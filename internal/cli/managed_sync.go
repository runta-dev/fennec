package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fennec/internal/launchprofile"
	"fennec/internal/syncengine"
	"fennec/internal/terminalstream"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func managedSyncState(home, owner, runtimeID string) string {
	scope := sha256.Sum256([]byte(owner + "\x00" + runtimeID))
	return filepath.Join(home, ".config/fennec/sync", hex.EncodeToString(scope[:16]))
}

func findMutagen() (string, error) {
	if path, err := exec.LookPath("mutagen"); err == nil {
		return path, nil
	}
	for _, path := range []string{"/opt/homebrew/bin/mutagen", "/usr/local/bin/mutagen"} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("Mutagen is not installed")
}

// The broker-ready callback holds s.mu. Migration is background work and never
// extends the interactive readiness path or creates a new project registration.
func (s *daemonServices) startSyncMigrationLocked() {
	if s.ctx == nil || s.user == "" || s.config.RuntimeID == "" || s.channel == nil || s.syncMigrationRuntime == s.config.RuntimeID {
		return
	}
	if s.syncMigrationCancel != nil {
		s.syncMigrationCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.syncMigrationCancel = cancel
	s.syncMigrationRuntime = s.config.RuntimeID
	owner, runtimeID, epoch, home := s.user, s.config.RuntimeID, s.epoch, s.home
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer cancel()
		executable, err := os.Executable()
		if err != nil {
			return
		}
		state := managedSyncState(home, owner, runtimeID)
		registry := filepath.Join(home, ".config/fennec/projects")
		for ctx.Err() == nil {
			s.mu.Lock()
			current := s.epoch == epoch && s.user == owner && s.config.RuntimeID == runtimeID
			channel := s.channel
			s.mu.Unlock()
			if !current {
				return
			}
			attempt, stop := context.WithTimeout(ctx, 45*time.Second)
			err := func() error {
				entries, err := syncengine.ListProjects(registry)
				if err != nil {
					return err
				}
				matched := false
				for _, entry := range entries {
					if entry.DataDirectory == filepath.Join(state, "data") && entry.SSHDirectory == filepath.Join(state, "ssh") {
						matched = true
						break
					}
				}
				if !matched {
					return nil
				}
				if channel == nil {
					return fmt.Errorf("resident unavailable for sync migration")
				}
				transport, err := channel.get(attempt)
				if err != nil {
					return err
				}
				if err = transport.Ready(attempt); err != nil {
					return err
				}
				if !transport.Supports(terminalstream.CapabilityCommand) {
					return fmt.Errorf("resident transfer capability unavailable")
				}
				s.mu.Lock()
				current = s.epoch == epoch && s.user == owner && s.config.RuntimeID == runtimeID && s.channel == channel
				s.mu.Unlock()
				if !current {
					return fmt.Errorf("sync migration scope changed")
				}
				binary, _ := findMutagen()
				manager := syncengine.Manager{Binary: binary, DataDirectory: filepath.Join(state, "data"), SSHDirectory: filepath.Join(state, "ssh")}
				return manager.MigrateManagedSessions(attempt, registry, executable, owner, runtimeID)
			}()
			stop()
			if err == nil {
				return
			}
			launchprofile.Record("sync.transport_migration_pending", time.Now(), 0)
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
