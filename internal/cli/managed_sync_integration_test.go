package cli

import (
	"bytes"
	"context"
	"fennec/internal/syncengine"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Use real Mutagen, scp, the CLI wrapper, control IPC, the resident protocol and
// the production SFTP executable. Both endpoints are isolated local fixtures;
// this verifies the complete adapter, not WAN latency or installed-app state.
func TestManagedMutagenOverResident(t *testing.T) {
	if os.Getenv("FENNEC_MUTAGEN_INTEGRATION") != "1" {
		t.Skip("explicit isolated Mutagen acceptance")
	}
	cliBinary, terminalBinary := os.Getenv("FENNEC_MUTAGEN_CLI"), os.Getenv("FENNEC_MUTAGEN_TERMINAL")
	if !filepath.IsAbs(cliBinary) || !filepath.IsAbs(terminalBinary) {
		t.Fatal("staged production CLI and terminal required")
	}
	mutagen, err := exec.LookPath("mutagen")
	if err != nil {
		t.Fatal(err)
	}
	s, ctx, _ := managedTransferFixture(t, 10*time.Minute)
	t.Setenv("HOME", s.home)
	s.mu.Lock()
	s.config.RuntimeID = "01a1b016-11bc-7e63-b3d6-c4955e17b73b"
	s.mu.Unlock()
	entry := filepath.Join(s.home, ".local/fennec/bin/fennec-terminal")
	if err = os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(terminalBinary, entry); err != nil {
		t.Fatal(err)
	}
	alpha, beta := filepath.Join(s.home, "alpha"), filepath.Join(s.home, "beta")
	for _, dir := range []string{alpha, beta} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = exec.CommandContext(ctx, "git", "-C", alpha, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0, 255, 7, 11}, 32768)
	if err = os.WriteFile(filepath.Join(alpha, "binary"), payload, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(alpha, ".env"), []byte("fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := syncengine.Manager{Binary: mutagen, DataDirectory: filepath.Join(s.home, "mutagen"), SSHDirectory: filepath.Join(s.home, "ssh")}
	alias, err := syncengine.ConfigureManagedSSH(manager.SSHDirectory, cliBinary, s.user, s.config.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// --all is confined to this test's newly-created private data directory.
		for _, args := range [][]string{{"sync", "terminate", "--all"}, {"daemon", "stop"}} {
			command := exec.CommandContext(cleanup, mutagen, args...)
			command.Env = append(os.Environ(), "MUTAGEN_DATA_DIRECTORY="+manager.DataDirectory, "MUTAGEN_SSH_PATH="+manager.SSHDirectory)
			if data, err := command.CombinedOutput(); err != nil {
				t.Logf("fixture cleanup: %v %s", err, data)
			}
		}
	}()
	var pings, maximum atomic.Int64
	pingCtx, stopPings := context.WithCancel(ctx)
	pingDone := make(chan error, 1)
	go func() {
		for pingCtx.Err() == nil {
			step, cancel := context.WithTimeout(pingCtx, time.Second)
			rtt, err := s.channel.transport.Ping(step)
			cancel()
			if err != nil {
				if pingCtx.Err() != nil {
					pingDone <- nil
				} else {
					pingDone <- err
				}
				return
			}
			pings.Add(1)
			for prior := maximum.Load(); int64(rtt) > prior && !maximum.CompareAndSwap(prior, int64(rtt)); prior = maximum.Load() {
			}
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-pingCtx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		pingDone <- nil
	}()
	defer func() {
		stopPings()
		if err := <-pingDone; err != nil {
			t.Error("managed sync starved control", err)
		}
	}()
	started := time.Now()
	registry := filepath.Join(s.home, "registry")
	var session syncengine.Session
	err = syncengine.WithRegistryLock(ctx, registry, func() error {
		var err error
		session, err = manager.EnsureManaged(ctx, registry, s.user, alpha, alias+":"+beta)
		return err
	})
	if err != nil {
		t.Fatal("initial managed agent installation", err)
	}
	if ready, err := manager.Flush(ctx, session.Name); err != nil || !ready.Ready() {
		t.Fatal("initial transfer not ready", err)
	}
	data, err := os.ReadFile(filepath.Join(beta, "binary"))
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("initial binary transfer changed", err)
	}
	info, err := os.Stat(filepath.Join(beta, "binary"))
	if err != nil || info.Mode()&0100 == 0 {
		t.Fatal("executable mode lost", err)
	}
	if _, err = os.Stat(filepath.Join(beta, ".env")); !os.IsNotExist(err) {
		t.Fatal("excluded credential copied", err)
	}
	if err = os.WriteFile(filepath.Join(beta, "cloud-edit"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Flush(ctx, session.Name); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(alpha, "cloud-edit"))
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("reverse transfer changed", err)
	}
	if err = os.Remove(filepath.Join(alpha, "binary")); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Flush(ctx, session.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(beta, "binary")); !os.IsNotExist(err) {
		t.Fatal("scoped deletion not synchronized", err)
	}
	// Simulate pre-upgrade registrations while retaining real running sessions.
	// Migration must release old connections without replacing IDs or files.
	for _, paused := range []bool{false, true} {
		if _, err = manager.SetRegisteredPaused(ctx, registry, session.Name, paused); err != nil {
			t.Fatal(err)
		}
		entries, err := syncengine.ListProjects(registry)
		if err != nil || len(entries) != 1 {
			t.Fatal(entries, err)
		}
		entry := entries[0]
		entry.Transport = ""
		if err = syncengine.Register(registry, entry); err != nil {
			t.Fatal(err)
		}
		if err = manager.MigrateManagedSessions(ctx, registry, cliBinary, s.user, s.config.RuntimeID); err != nil {
			t.Fatal("existing session migration", err)
		}
		states, err := manager.Inspect(ctx, session.Name)
		if err != nil || len(states) != 1 || states[0].Identifier != session.Identifier || states[0].Paused != paused {
			t.Fatal("migration replaced session or changed pause state", states, err)
		}
	}
	if _, err = manager.SetRegisteredPaused(ctx, registry, session.Name, false); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Flush(ctx, session.Name); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{alpha, beta} {
		data, err := os.ReadFile(filepath.Join(dir, "cloud-edit"))
		if err != nil || !bytes.Equal(data, payload) {
			t.Fatal("migration changed project bytes", err)
		}
	}
	if pings.Load() < 5 {
		t.Fatal("insufficient loaded ping evidence")
	}
	t.Logf("cold agent installation, two-way transfer, deletion and active/paused migration verified in %s; %d exact pings, maximum %s", time.Since(started), pings.Load(), time.Duration(maximum.Load()))
}
