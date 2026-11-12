package localssh

import (
	"os"
	"path/filepath"
	"testing"

	"fennec/internal/sandbox"
)

func TestCloudEscalateForwardsToTheMacUnchanged(t *testing.T) {
	for _, tc := range []struct {
		args          []string
		host, command string
	}{
		{[]string{"--reason", "list keys", "--", "ls ~/.ssh"}, "machine", "ls ~/.ssh"},
		{[]string{"--reason=need it", "--", "echo", "a", "b"}, "machine", "echo a b"},
		{[]string{"--host", "Shiqis-MacBook-Pro.local", "--reason", "x", "--", "echo 'q'; ls"}, "Shiqis-MacBook-Pro.local", "echo 'q'; ls"},
	} {
		host, remote, err := CloudEscalateRemote(tc.args)
		if err != nil || host != tc.host {
			t.Fatalf("%v: %q %v", tc.args, host, err)
		}
		inner, _, ok := sandbox.ParseEscalation(remote)
		if !ok || inner != tc.command {
			t.Fatalf("remote %q parsed on the Mac as %q", remote, inner)
		}
	}
	for _, bad := range [][]string{{"--", "ls"}, {"--reason", "x"}, {"--reason", "x", "--"}, {"--host", "-oProxyCommand=x", "--reason", "x", "--", "ls"}, {"ls"}} {
		if _, _, err := CloudEscalateRemote(bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}

func TestInstallEscalateEntryKeepsUserFiles(t *testing.T) {
	home := t.TempDir()
	if err := InstallEscalateEntry(home); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(home, ".local", "bin", "fennec-escalate")
	if target, err := os.Readlink(entry); err != nil || target != filepath.Join(home, ".local", "fennec", "bin", "fennec-terminal") {
		t.Fatalf("entry %q %v", target, err)
	}
	if err := InstallEscalateEntry(home); err != nil {
		t.Fatal("reinstall must be idempotent")
	}
	os.Remove(entry)
	os.WriteFile(entry, []byte("#!/bin/sh\necho mine\n"), 0o755)
	if err := InstallEscalateEntry(home); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(entry); string(data) != "#!/bin/sh\necho mine\n" {
		t.Fatal("a user's own fennec-escalate must not be replaced")
	}
}
