package cli

import (
	"context"
	"fennec/internal/launcher"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func createTrashFixtureApp(t *testing.T, appPath string) string {
	t.Helper()
	macOS := filepath.Join(appPath, "Contents/MacOS")
	if err := os.MkdirAll(macOS, 0755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runta.fennec</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(appPath, "Contents/Info.plist"), []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(macOS, "fen")
	if err := os.WriteFile(daemon, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return daemon
}

func TestTrashWatchIgnoresUpgradeAndCleansUpWhenAppMovedToTrash(t *testing.T) {
	home := t.TempDir()
	appsDir := filepath.Join(home, "Apps")
	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0700); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(appsDir, "Fennec.app")
	daemon := createTrashFixtureApp(t, appPath)

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	realCodex := filepath.Join(binDir, "codex-real")
	if err := os.WriteFile(realCodex, []byte("#!/bin/sh\necho local-codex\n"), 0755); err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(binDir, "codex")
	if err := os.Symlink(realCodex, entryPath); err != nil {
		t.Fatal(err)
	}
	if err := launcher.InstallEntry(entryPath, daemon, "codex"); err != nil {
		t.Fatal(err)
	}
	shimsDir := filepath.Join(home, ".local/share/fennec/shims")
	if err := launcher.InstallShims(shimsDir, daemon, binDir); err != nil {
		t.Fatal(err)
	}
	customZdot := filepath.Join(home, "custom-zsh")
	if err := os.MkdirAll(customZdot, 0700); err != nil {
		t.Fatal(err)
	}
	customZshrc := filepath.Join(customZdot, ".zshrc")
	if err := os.WriteFile(customZshrc, []byte("export KEEP_USER=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZDOTDIR", customZdot)
	if err := launcher.InstallShellIntegration(home, "/bin/zsh"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZDOTDIR", "")

	launchAgents := filepath.Join(home, "Library/LaunchAgents")
	if err := os.MkdirAll(launchAgents, 0700); err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(launchAgents, daemonLabel+".plist")
	if err := os.WriteFile(plistPath, daemonPlist(daemon, filepath.Join(home, ".config/fennec/logs/daemon.log")), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var currentExe atomic.Value
	currentExe.Store(daemon)
	var bootedOut atomic.Int32
	removed := make(chan struct{})
	go watchInstalledApplication(ctx, 10*time.Millisecond, home, appPath, func() string { return currentExe.Load().(string) }, func(string) []string {
		return []string{entryPath}
	}, func() {
		bootedOut.Add(1)
	}, func() {
		close(removed)
		cancel()
	})

	staging := filepath.Join(appsDir, ".fennec-update-123/incoming.app")
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(35 * time.Millisecond)
	select {
	case <-removed:
		t.Fatal("active upgrade triggered Trash cleanup")
	default:
	}
	_ = os.RemoveAll(filepath.Dir(staging))

	trashedApp := filepath.Join(trashDir, "Fennec.app")
	if err := os.Rename(appPath, trashedApp); err != nil {
		t.Fatal(err)
	}
	currentExe.Store(filepath.Join(trashedApp, "Contents/MacOS/fen"))

	select {
	case <-removed:
	case <-time.After(2 * time.Second):
		t.Fatal("dragging app to Trash did not stop and unregister background daemon")
	}

	link, err := os.Readlink(entryPath)
	if err != nil || link != realCodex {
		t.Fatalf("managed entry was not restored to original symlink: link=%q err=%v", link, err)
	}
	zshrcData, err := os.ReadFile(customZshrc)
	if err != nil || strings.Contains(string(zshrcData), "Fennec CLI integration") || !strings.Contains(string(zshrcData), "export KEEP_USER=1") {
		t.Fatalf("custom ZDOTDIR/.zshrc was not cleanly restored: %q %v", zshrcData, err)
	}
	if _, err := os.Stat(filepath.Join(shimsDir, "codex")); !os.IsNotExist(err) {
		t.Fatalf("managed shim survived Trash cleanup: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("LaunchAgent plist survived Trash cleanup: %v", err)
	}
	if bootedOut.Load() != 1 {
		t.Fatalf("expected exactly 1 launchd bootout trigger, got %d", bootedOut.Load())
	}
}
