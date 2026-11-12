package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Approved escalation may list ~/.ssh and read public material; private keys
// and writes stay denied. Without escalation the whole directory is denied.
func TestApprovedEscalationReadsOnlyPublicSSHFiles(t *testing.T) {
	if !Available() {
		t.Skip("Seatbelt unavailable")
	}
	home, workspace := t.TempDir(), t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	ssh := filepath.Join(home, ".ssh")
	os.MkdirAll(filepath.Join(ssh, "config.d"), 0o700)
	files := map[string]string{"id_ed25519": "PRIVATE", "id_ed25519.pub": "ssh-ed25519 AAAA", "config": "Host x", "config.d/work": "Host y", "known_hosts": "h", "authorized_keys": "k", "google_compute_engine": "PRIVATE"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(ssh, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(policy Policy, script string) int {
		cmd, _ := Command(context.Background(), policy, home, workspace, script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(interface{ ExitCode() int }); ok {
				return exit.ExitCode()
			}
			return -1
		}
		return 0
	}
	workspacePolicy := PolicyForPreset(PresetAsk, []string{workspace})
	escalated := ForApprovedEscalation(workspacePolicy)
	for _, name := range []string{"id_ed25519.pub", "config", "config.d/work", "known_hosts", "authorized_keys"} {
		if code := run(escalated, "cat "+filepath.Join(ssh, name)+" >/dev/null"); code != 0 {
			t.Fatalf("approved escalation should read %s (exit %d)", name, code)
		}
		if code := run(workspacePolicy, "cat "+filepath.Join(ssh, name)+" >/dev/null"); code == 0 {
			t.Fatalf("unescalated command read %s", name)
		}
	}
	if code := run(escalated, "ls "+ssh+" >/dev/null"); code != 0 {
		t.Fatal("approved escalation should list ~/.ssh")
	}
	for _, name := range []string{"id_ed25519", "google_compute_engine"} {
		if code := run(escalated, "cat "+filepath.Join(ssh, name)+" >/dev/null"); code == 0 {
			t.Fatalf("approved escalation read private key %s", name)
		}
	}
	if code := run(escalated, "touch "+filepath.Join(ssh, "new")); code == 0 {
		t.Fatal("approved escalation wrote into ~/.ssh")
	}
	if code := run(escalated, "echo x >> "+filepath.Join(ssh, "config")); code == 0 {
		t.Fatal("approved escalation modified ~/.ssh/config")
	}
}
