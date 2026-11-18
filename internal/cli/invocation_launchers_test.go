package cli

import (
	"strings"
	"testing"

	"fennec/internal/launcher"
)

// Launchers and the cleanup agent run the binary named fen (the stable
// dispatcher, the bundle's fen); every internal command they use must not be
// read as a public `fen` subcommand. 0.5.3 shipped dispatch-stable as public,
// so every agent shell function printed fen's usage instead of starting.
func TestLauncherCommandsAreInternal(t *testing.T) {
	commands := map[string]bool{"cleanup-after-removal": true}
	script, err := launcher.ShellIntegration("zsh", "/tmp/shims", "/tmp/dispatcher/fen")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(script) {
		if strings.HasPrefix(field, "dispatch-") {
			commands[field] = true
		}
	}
	commands["dispatch-agent"], commands["dispatch-entry"] = true, true
	if !commands["dispatch-stable"] {
		t.Fatal("shell functions no longer call dispatch-stable; update this test")
	}
	for command := range commands {
		if PublicInvocation([]string{command, "codex"}) {
			t.Errorf("%s is routed to the public fen command", command)
		}
	}
	if !strings.Contains(string(cleanupPlist("/tmp/fen", "/Applications/Fennec.app", "/dev/null")), "<string>cleanup-after-removal</string>") {
		t.Fatal("cleanup agent command changed; update this test")
	}
}
