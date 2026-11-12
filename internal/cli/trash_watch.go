package cli

import (
	"bytes"
	"context"
	"fennec/internal/agent"
	"fennec/internal/installation"
	"fennec/internal/launcher"
	"fennec/internal/uninstall"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// installedBundleForTrashWatch returns the canonical .app bundle path only when
// control-daemon was launched from a real installed macOS bundle (never /tmp,
// Trash, or a repository build/ staging bundle).
func installedBundleForTrashWatch(arg0, home string) string {
	if runtime.GOOS != "darwin" || !filepath.IsAbs(arg0) {
		return ""
	}
	cleaned := filepath.Clean(arg0)
	if filepath.Base(filepath.Dir(cleaned)) != "MacOS" {
		return ""
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(cleaned)))
	if !strings.EqualFold(filepath.Ext(app), ".app") {
		return ""
	}
	if installation.RequiresInstallation(app, os.TempDir(), false) {
		return ""
	}
	if filepath.Base(filepath.Dir(app)) == "build" {
		if mod, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(app)), "go.mod")); err == nil && bytes.HasPrefix(mod, []byte("module fennec\n")) {
			return ""
		}
	}
	if id, err := uninstall.BundleID(app); err != nil || id != "com.runta.fennec" {
		return ""
	}
	if home != "" && strings.HasPrefix(filepath.Base(filepath.Dir(app)), ".fennec-update-") {
		return ""
	}
	return app
}

func pathHasTrashSegment(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == ".Trash" || part == ".Trashes" {
			return true
		}
	}
	return false
}

func bundleRemovedOrTrashed(app string, currentExecutable func() string) bool {
	parent := filepath.Dir(app)
	if matches, _ := filepath.Glob(filepath.Join(parent, ".fennec-update-*", "incoming.app")); len(matches) > 0 {
		return false
	}
	if currentExecutable != nil {
		if exe := currentExecutable(); exe != "" {
			if pathHasTrashSegment(exe) {
				return true
			}
			if resolved, err := filepath.EvalSymlinks(exe); err == nil && pathHasTrashSegment(resolved) {
				return true
			}
		}
	}
	daemon := filepath.Join(app, "Contents/MacOS/fen")
	if info, err := os.Stat(daemon); err != nil || !info.Mode().IsRegular() {
		return true
	}
	if id, err := uninstall.BundleID(app); err != nil || id != "com.runta.fennec" {
		return true
	}
	return false
}

func cleanupLocalAfterTrash(home, app string, entries []string, bootout func()) {
	for _, entry := range entries {
		_ = launcher.UninstallEntry(entry)
	}
	shellCandidates := map[string]bool{}
	for _, name := range []string{".zshrc", ".bashrc", ".bash_profile", ".bash_login", ".profile", ".config/fish/conf.d/fennec.fish"} {
		shellCandidates[filepath.Join(home, name)] = true
	}
	for _, recorded := range launcher.RecordedShellFiles(home) {
		shellCandidates[recorded] = true
	}
	for path := range shellCandidates {
		_ = launcher.UninstallShellFile(path)
	}
	shimsDir := filepath.Join(home, ".local/share/fennec/shims")
	if items, err := os.ReadDir(shimsDir); err == nil {
		for _, item := range items {
			if item.IsDir() {
				continue
			}
			full := filepath.Join(shimsDir, item.Name())
			if data, err := os.ReadFile(full); err == nil && strings.HasPrefix(string(data), "#!/bin/sh\n# Fennec managed launcher v1\n") {
				_ = os.Remove(full)
			}
		}
		_ = os.Remove(shimsDir)
	}
	_ = os.Remove(filepath.Join(home, ".local/share/fennec/shell-init.sh"))
	_ = os.Remove(filepath.Join(home, ".local/share/fennec/shell-files.json"))

	plist := filepath.Join(home, "Library/LaunchAgents", daemonLabel+".plist")
	expectedDaemon := filepath.Join(app, "Contents/MacOS/fen")
	if data, err := os.ReadFile(plist); err == nil && bytes.Contains(data, []byte("<string>"+expectedDaemon+"</string>")) {
		_ = os.Remove(plist)
		if bootout != nil {
			bootout()
		}
	}
}

func watchInstalledApplication(ctx context.Context, interval time.Duration, home, app string, currentExecutable func() string, entries func(string) []string, bootout func(), onRemoved func()) {
	if app == "" {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if uninstallRunning(home) || uninstall.Pending(home) {
				misses = 0
				continue
			}
			if !bundleRemovedOrTrashed(app, currentExecutable) {
				misses = 0
				continue
			}
			misses++
			if misses < 2 {
				continue
			}
			var managed []string
			if entries != nil {
				managed = entries(home)
			}
			cleanupLocalAfterTrash(home, app, managed, bootout)
			if onRemoved != nil {
				onRemoved()
			}
			return
		}
	}
}

func startTrashWatcher(ctx context.Context, cancel context.CancelFunc, home, arg0 string) {
	app := installedBundleForTrashWatch(arg0, home)
	if app == "" {
		return
	}
	go watchInstalledApplication(ctx, 2*time.Second, home, app, func() string {
		exe, _ := os.Executable()
		return exe
	}, agent.ManagedEntryPaths, func() {
		if runtime.GOOS == "darwin" {
			target := daemonTarget()
			go func() {
				time.Sleep(150 * time.Millisecond)
				_ = exec.Command("/bin/launchctl", "bootout", target).Run()
			}()
		}
	}, cancel)
}
