package syncengine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const managedTestRuntime = "01a1b016-11bc-7e63-b3d6-c4955e17b73b"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "transfer-ssh" {
		json.NewEncoder(os.Stdout).Encode(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestManagedSSHWrapperPreservesScopeAndArguments(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "wrapper's directory")
	alias, err := ConfigureManagedSSH(dir, binary, "owner's identity", managedTestRuntime)
	if err != nil {
		t.Fatal(err)
	}
	command := `printf '%s' 'argument with spaces'`
	output, err := exec.Command(filepath.Join(dir, "ssh"), "-oConnectTimeout=5", alias, command).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"transfer-ssh", "owner's identity", managedTestRuntime, "--", "-oConnectTimeout=5", "fennec-cloud", command}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatal(got)
	}
	scp, err := os.ReadFile(filepath.Join(dir, "scp"))
	if err != nil || !strings.Contains(string(scp), "exec /usr/bin/scp -S "+quoteShell(filepath.Join(dir, "ssh"))) {
		t.Fatal("installer bypasses managed wrapper", err)
	}
}

func TestManagedSSHAcceptsMutagenAndSFTPInvocations(t *testing.T) {
	for _, args := range [][]string{
		{"-oConnectTimeout=5", "-oServerAliveInterval=10", "-oServerAliveCountMax=1", "fennec-cloud", "agent synchronize"},
		{"-p", "22", "-l", managedTestRuntime, "-T", "--", "fennec-cloud", "printf", "'%s'", "'a b'"},
	} {
		got, err := ManagedSSHCommand(args, managedTestRuntime)
		if err != nil || got == "" {
			t.Fatalf("%v: %s %v", args, got, err)
		}
	}
	args := []string{"-x", "-oPermitLocalCommand=no", "-oClearAllForwardings=yes", "-oRemoteCommand=none", "-oRequestTTY=no", "-oForwardAgent=no", "-s", "--", "fennec-cloud", "sftp"}
	got, err := ManagedSSHCommand(args, managedTestRuntime)
	if err != nil || got != `"$HOME/.local/fennec/bin/fennec-terminal" sftp-stdio` {
		t.Fatal(got, err)
	}
}

func TestManagedSSHRejectsRedirectsAndScopeChanges(t *testing.T) {
	for _, args := range [][]string{
		{"-p", "2222", "fennec-cloud", "cat"},
		{"-l", "someone-else", "fennec-cloud", "cat"},
		{"-oProxyCommand=bad", "fennec-cloud", "cat"},
		{"-oStrictHostKeyChecking=no", "fennec-cloud", "cat"},
		{"-oForwardAgent=yes", "fennec-cloud", "cat"},
		{"-L", "99:localhost:1", "fennec-cloud", "cat"},
		{"other-host", "cat"},
		{"-s", "fennec-cloud", "other-subsystem"},
		{"fennec-cloud"},
		{"-o"},
		{"fennec-cloud", strings.Repeat("x", 128<<10)},
	} {
		if _, err := ManagedSSHCommand(args, managedTestRuntime); err == nil {
			t.Errorf("accepted unsupported invocation: %q", args[:min(len(args), 2)])
		}
	}
}
