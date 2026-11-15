package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/launchprofile"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fmt"
	"path/filepath"
	"time"
)

type magpieCredentialSync struct {
	last   string
	gather func() (map[string]string, error)
	push   func(context.Context, map[string]string) error
}

func magpieCredentialDigest(files map[string]string) (string, error) {
	content := map[string]string{}
	for name, encoded := range files {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", err
		}
		// Magpie's last-seen timestamp and JSON formatting are not changes to
		// account material. This is the same normalization preparation uses.
		content[name] = string(preparationCredentialContent(name, data))
	}
	data, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (syncer *magpieCredentialSync) step(ctx context.Context) error {
	files, err := syncer.gather()
	if err != nil {
		return err
	}
	digest, err := magpieCredentialDigest(files)
	if err != nil {
		return err
	}
	if digest == syncer.last {
		return nil
	}
	if len(files) == 0 {
		syncer.last = digest
		return nil
	} // Absence never deletes cloud accounts.
	if err = syncer.push(ctx, files); err != nil {
		return err
	}
	syncer.last = digest
	return nil
}

func (s *daemonServices) startMagpieCredentialSync(ctx context.Context, epoch uint64) {
	paths := []string{connectionWakePath(s.home)}
	for _, name := range []string{"providers.json", "logins.json", "plugin-auth.json"} {
		paths = append(paths, filepath.Join(s.home, ".config/magpie", name))
	}
	changes := setup.WatchFiles(ctx, paths, time.Minute)
	syncer := magpieCredentialSync{gather: func() (map[string]string, error) { return setup.GatherMagpieCredentials(s.home) }}
	// Initialize synchronously before preparation starts gathering its inputs.
	// A goroutine here could run after preparation and acknowledge an edit that
	// was never sent. Watch before reading to retain initialization-time edits.
	if newDesktopSettings(s.home).value.Magpie {
		if files, err := syncer.gather(); err == nil {
			syncer.last, _ = magpieCredentialDigest(files)
		}
	}
	syncer.push = func(ctx context.Context, files map[string]string) error {
		if !newDesktopSettings(s.home).value.Magpie {
			return fmt.Errorf("gateway disabled")
		}
		s.mu.Lock()
		channel, current, ready := s.channel, s.epoch, s.brokerReady
		s.mu.Unlock()
		if current != epoch || channel == nil || !ready {
			return fmt.Errorf("cloud is reconnecting")
		}
		transport := channel.peekAlive()
		if transport == nil || !transport.Supports("magpie_credentials_sync") {
			return fmt.Errorf("credential sync unavailable")
		}
		step, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		started := time.Now()
		data, err := transport.Operate(step, cloudruntime.LocalOperation{Kind: "magpie_credentials_sync", MagpieCredentials: files})
		if err != nil {
			return err
		}
		var report setup.CredentialMigrationReport
		if json.Unmarshal(data, &report) != nil || report.Received != len(files) {
			return fmt.Errorf("credential sync unconfirmed")
		}
		launchprofile.Record("credential_sync.magpie", started, time.Since(started))
		return nil
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		syncer.run(ctx, changes, func() bool { return newDesktopSettings(s.home).value.Magpie })
	}()
}

func (syncer *magpieCredentialSync) run(ctx context.Context, changes <-chan struct{}, enabled func() bool) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case _, ok := <-changes:
			if !ok {
				changes = nil
				continue
			}
			// Merge atomic replacements and bursts from a single sign-in.
			if !sleepContext(ctx, 150*time.Millisecond) {
				return
			}
		}
		if enabled() {
			_ = syncer.step(ctx) // Pending content survives an outage; no background UI errors.
		}
	}
}
