package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"fennec/internal/agent"
	"fennec/internal/installation"
	"fennec/internal/launcher"
	"fennec/internal/uninstall"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The cleanup agent restores the user's environment after the application
// is moved to the Trash, whether or not the daemon is running then: it is a
// separate LaunchAgent that runs the stable dispatcher (outside the bundle) at
// each login and whenever the application's location changes.
const cleanupLabel = "com.runta.fennec.cleanup"

func cleanupPlistPath(home string) string {
	return filepath.Join(home, "Library/LaunchAgents", cleanupLabel+".plist")
}

func cleanupTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + cleanupLabel }

func cleanupPlist(stable, app, logPath string) []byte {
	escape := func(s string) string { var out bytes.Buffer; xml.EscapeText(&out, []byte(s)); return out.String() }
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + cleanupLabel + `</string><key>ProgramArguments</key><array><string>` + escape(stable) + `</string><string>cleanup-after-removal</string></array><key>RunAtLoad</key><true/><key>WatchPaths</key><array><string>` + escape(app) + `</string><string>` + escape(filepath.Dir(app)) + `</string></array><key>ThrottleInterval</key><integer>10</integer><key>ProcessType</key><string>Background</string><key>StandardErrorPath</key><string>` + escape(logPath) + `</string></dict></plist>`)
}

// ensureCleanupAgent registers the cleanup agent for the installed app.
func ensureCleanupAgent(ctx context.Context, home, stable, app string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	logs := filepath.Join(home, "Library/Logs/Fennec")
	if err := os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	desired := cleanupPlist(stable, app, filepath.Join(logs, "cleanup.log"))
	path := cleanupPlistPath(home)
	current, _ := os.ReadFile(path)
	loaded := exec.CommandContext(ctx, "/bin/launchctl", "print", cleanupTarget()).Run() == nil
	if bytes.Equal(current, desired) && loaded {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".fennec-cleanup-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(desired); err == nil {
		err = file.Chmod(0644)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	if loaded {
		_ = exec.CommandContext(ctx, "/bin/launchctl", "bootout", cleanupTarget()).Run()
	}
	if err = exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path).Run(); err != nil {
		return fmt.Errorf("unable to register the cleanup agent")
	}
	return nil
}

func cleanupLog(home, message string) {
	path := filepath.Join(home, "Library/Logs/Fennec/cleanup.log")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), message)
		file.Close()
	}
}

// otherInstalledApplication reports a Fennec bundle at a standard location
// other than app: the user moved the application rather than removed it.
func otherInstalledApplication(home, app string) bool {
	for _, candidate := range []string{"/Applications/Fennec.app", filepath.Join(home, "Applications/Fennec.app")} {
		if filepath.Clean(candidate) == filepath.Clean(app) {
			continue
		}
		if id, err := uninstall.BundleID(candidate); err == nil && id == "com.runta.fennec" {
			return true
		}
	}
	return false
}

// cleanupAfterRemoval is the cleanup agent's command. It acts only when the
// recorded application is gone or in a Trash, no update is replacing it, no
// other Fennec is installed, and two observations two seconds apart agree.
func cleanupAfterRemoval() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	executable := launcher.RegisteredExecutable(home)
	if executable == "" {
		return nil
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	removed := func() bool {
		if launcher.ApplicationPresent(home) || otherInstalledApplication(home, app) {
			return false
		}
		return !installation.UpdateInProgress(filepath.Dir(app))
	}
	if !removed() {
		return nil
	}
	time.Sleep(2 * time.Second)
	if !removed() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cleanupLog(home, "application removed: "+app)
	performRemovalCleanup(ctx, home, app, agent.ManagedEntryPaths(home), true, func() {
		_ = exec.CommandContext(ctx, "/bin/launchctl", "bootout", daemonTarget()).Run()
	})
	return nil
}

// restoreCodexDesktop returns Codex Desktop to its local app-server: the
// bridge variable leaves launchd, and a ChatGPT.app still started with it is
// quit (gracefully, as Quit does) and reopened without it.
func restoreCodexDesktop(ctx context.Context, home string) {
	if runtime.GOOS != "darwin" {
		return
	}
	out, _ := exec.CommandContext(ctx, "/bin/launchctl", "getenv", chatgptEnvironment).Output()
	if value := strings.TrimSpace(string(out)); strings.HasPrefix(value, "ws://127.0.0.1:") && strings.Contains(value, chatgptPathPrefix) {
		if err := exec.CommandContext(ctx, "/bin/launchctl", "unsetenv", chatgptEnvironment).Run(); err != nil {
			cleanupLog(home, "launchctl unsetenv failed: "+err.Error())
		}
	}
	if restarted, err := restartChatGPT(ctx, ""); err != nil {
		cleanupLog(home, "ChatGPT restart: "+err.Error())
	} else if restarted {
		cleanupLog(home, "ChatGPT reopened without the bridge")
	}
	_ = os.Remove(filepath.Join(home, ".config/fennec/chatgpt.json"))
}

// performRemovalCleanup restores the user's environment, in reverse order of
// installation. Each step leaves alone anything that is no longer what Fennec
// wrote. removeSelf also removes the stable dispatcher and the cleanup agent.
// bootoutDaemon stops a daemon whose registration was removed; it runs after
// every file step, since the daemon itself may be the caller.
func performRemovalCleanup(ctx context.Context, home, app string, entries []string, removeSelf bool, bootoutDaemon func()) {
	restoreCodexDesktop(ctx, home)
	_ = os.Remove(filepath.Join(home, ".config/fennec/takeover.json"))
	// What the last preparation recorded about the cloud computer. A reinstall
	// prepares it again from scratch instead of trusting a computer that may
	// have been deleted or recreated meanwhile (FEN-25: the card said "Cloud
	// computer ready" while Runta was still creating a new one). The cloud
	// computer itself is kept.
	for _, path := range []string{checkpointPath(home), installationCheckpointPath(home)} {
		_ = os.Remove(path)
	}
	for _, name := range []string{"preparation.json", "preparation-cache-status.json", "route-results.json"} {
		_ = os.Remove(filepath.Join(home, ".config/fennec", name))
	}
	for _, entry := range entries {
		if err := launcher.UninstallEntry(entry); err != nil {
			cleanupLog(home, "entry "+entry+": "+err.Error())
		}
	}
	shellCandidates := map[string]bool{}
	for _, name := range []string{".zshenv", ".zshrc", ".bashrc", ".bash_profile", ".bash_login", ".profile", ".config/fish/conf.d/fennec.fish"} {
		shellCandidates[filepath.Join(home, name)] = true
	}
	for _, recorded := range launcher.RecordedShellFiles(home) {
		shellCandidates[recorded] = true
	}
	for path := range shellCandidates {
		if err := launcher.UninstallShellFile(path); err != nil {
			cleanupLog(home, "shell "+path+": "+err.Error())
		}
	}
	launcher.RemoveCreatedEmptyShellFiles(home)
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
	for _, name := range []string{"shell-init.sh", "env-init.sh", "shell-files.json"} {
		_ = os.Remove(filepath.Join(home, ".local/share/fennec", name))
	}
	plist := filepath.Join(home, "Library/LaunchAgents", daemonLabel+".plist")
	expectedDaemon := filepath.Join(app, "Contents/MacOS/fen")
	daemonRemoved := false
	if data, err := os.ReadFile(plist); err == nil && bytes.Contains(data, []byte("<string>"+expectedDaemon+"</string>")) {
		_ = os.Remove(plist)
		daemonRemoved = true
	}
	// Previous bundles kept by updates, and interrupted installs.
	for _, dir := range installation.PruneStaging(filepath.Dir(app)) {
		cleanupLog(home, "removed "+dir)
	}
	if removeSelf {
		// FEN-29: removing the application removes its configuration too
		// (account, settings, caches), so a reinstall starts fresh, e.g. in
		// the system language. Logs, the Keychain item and the cloud computer
		// are kept.
		if runtime.GOOS == "darwin" {
			_ = exec.CommandContext(ctx, "/usr/bin/defaults", "delete", "com.runta.fennec").Run()
		}
		if err := uninstall.RemoveConfiguration(home); err != nil {
			cleanupLog(home, "configuration: "+err.Error())
		}
		_ = os.RemoveAll(launcher.StableDir(home))
		_ = os.Remove(cleanupPlistPath(home))
		cleanupLog(home, "cleanup complete")
	}
	if daemonRemoved && bootoutDaemon != nil {
		bootoutDaemon()
	}
	if removeSelf && runtime.GOOS == "darwin" {
		// Last: this may end the running cleanup process itself.
		_ = exec.CommandContext(ctx, "/bin/launchctl", "bootout", cleanupTarget()).Run()
	}
}
