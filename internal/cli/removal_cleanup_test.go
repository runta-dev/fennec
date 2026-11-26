package cli

import (
	"context"
	"fennec/internal/launcher"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Removal after the application is trashed restores the user's startup
// files byte for byte, removes Fennec's configuration, the stable
// dispatcher and both LaunchAgent registrations (not its logs), and stops
// the daemon only after every file step.
func TestRemovalCleanupRestoresTheUsersEnvironment(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	home := t.TempDir()
	app := filepath.Join(home, ".Trash/Fennec.app")
	original := "# mine\nexport EDITOR=vim\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	stable := launcher.StableExecutable(home)
	if err := os.MkdirAll(filepath.Dir(stable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	report := launcher.InstallShellTargets(launcher.ShellTargets{Home: home, Shell: zsh, Dispatcher: stable})
	if len(report.Written) != 2 {
		t.Fatalf("written %v", report.Written)
	}
	shims := filepath.Join(home, ".local/share/fennec/shims")
	if err := os.MkdirAll(shims, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(shims, "codex"), []byte("#!/bin/sh\n# Fennec managed launcher v1\n"), 0755)
	os.WriteFile(filepath.Join(shims, "mine"), []byte("#!/bin/sh\n"), 0755)
	os.MkdirAll(filepath.Join(home, ".config/fennec"), 0700)
	os.WriteFile(filepath.Join(home, ".config/fennec/takeover.json"), []byte(`{"agents":["codex"]}`), 0600)
	for _, name := range []string{"prepared-runtime.json", "installed-runtime.json", "preparation.json", "preparation-cache-status.json", "route-results.json"} {
		os.WriteFile(filepath.Join(home, ".config/fennec", name), []byte(`{}`), 0600)
	}
	os.WriteFile(filepath.Join(home, ".config/fennec/settings.json"), []byte(`{"language":"zh-CN"}`), 0600)
	for _, dir := range []string{"Library/Caches/com.runta.fennec", "Library/WebKit/com.runta.fennec", "Library/HTTPStorages/com.runta.fennec", "Library/Logs/Fennec", ".config/other"} {
		os.MkdirAll(filepath.Join(home, dir), 0700)
		os.WriteFile(filepath.Join(home, dir, "data"), []byte("x"), 0600)
	}
	agents := filepath.Join(home, "Library/LaunchAgents")
	os.MkdirAll(agents, 0700)
	os.WriteFile(cleanupPlistPath(home), cleanupPlist(stable, app, "/dev/null"), 0644)
	os.WriteFile(filepath.Join(agents, daemonLabel+".plist"), daemonPlist(filepath.Join(app, "Contents/MacOS/fen"), "/dev/null"), 0644)

	stopped := 0
	performRemovalCleanup(context.Background(), home, app, nil, true, func() {
		if _, err := os.Stat(launcher.StableDir(home)); err == nil {
			t.Error("daemon stopped before the file steps finished")
		}
		stopped++
	})
	if data, _ := os.ReadFile(filepath.Join(home, ".zshrc")); string(data) != original {
		t.Fatalf(".zshrc: %q", data)
	}
	for _, gone := range []string{".zshenv", ".local/share/fennec/shims/codex", ".local/share/fennec/shell-init.sh", ".local/share/fennec/env-init.sh", ".config/fennec/takeover.json", ".config/fennec/prepared-runtime.json", ".config/fennec/installed-runtime.json", ".config/fennec/preparation.json", ".config/fennec/preparation-cache-status.json", ".config/fennec/route-results.json", "Library/LaunchAgents/" + daemonLabel + ".plist", "Library/LaunchAgents/" + cleanupLabel + ".plist"} {
		if _, err := os.Lstat(filepath.Join(home, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still present", gone)
		}
	}
	if _, err := os.Stat(launcher.StableDir(home)); !os.IsNotExist(err) {
		t.Error("stable dispatcher still present")
	}
	// FEN-29: the configuration goes with the application, so a reinstall
	// starts fresh (e.g. in the system language); logs and others' files stay.
	for _, gone := range []string{".config/fennec", ".local/share/fennec", "Library/Caches/com.runta.fennec", "Library/WebKit/com.runta.fennec", "Library/HTTPStorages/com.runta.fennec", "Library/Application Support/com.runta.fennec"} {
		if _, err := os.Lstat(filepath.Join(home, gone)); !os.IsNotExist(err) {
			t.Errorf("configuration %s still present", gone)
		}
	}
	for _, kept := range []string{"Library/Logs/Fennec/data", ".config/other/data"} {
		if _, err := os.Stat(filepath.Join(home, kept)); err != nil {
			t.Errorf("%s removed", kept)
		}
	}
	if stopped != 1 {
		t.Fatalf("daemon stopped %d times", stopped)
	}
}

func TestAgentForCommand(t *testing.T) {
	for command, want := range map[string]string{"codex": "codex", "cursor-agent": "cursor", "claude": "claude", "vim": ""} {
		if got := agentForCommand(command); got != want {
			t.Errorf("%s: %q, want %q", command, got, want)
		}
	}
}

// A stale incoming.app left by an interrupted update (Nov 15 on a real Mac)
// made the Trash watcher treat every removal as an update in progress.
func TestTrashDetectionIgnoresAbandonedUpdate(t *testing.T) {
	parent := t.TempDir()
	app := filepath.Join(parent, "Fennec.app")
	incoming := filepath.Join(parent, ".fennec-update-1/incoming.app")
	os.MkdirAll(incoming, 0755)
	old := time.Now().Add(-72 * time.Hour)
	os.Chtimes(incoming, old, old)
	os.Chtimes(filepath.Dir(incoming), old, old)
	if !bundleRemovedOrTrashed(app, nil) {
		t.Fatal("a removed application was not detected beside an abandoned update")
	}
}
