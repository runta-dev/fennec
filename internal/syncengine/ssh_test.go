package syncengine

import (
	"fennec/internal/cloudtarget"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateSSHConfiguration(t *testing.T) {
	t.Setenv("FENNEC_RUNTA_ENV", "production")
	root := t.TempDir()
	key := filepath.Join(root, "device key")
	hosts := filepath.Join(root, "known hosts")
	os.WriteFile(key, []byte("unused"), 0600)
	os.WriteFile(hosts, nil, 0600)
	dir := filepath.Join(root, "wrapper's directory")
	alias, err := ConfigureSSH(dir, "01a1b016-11bc-7e63-b3d6-c4955e17b73b", key, hosts)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(filepath.Join(dir, "ssh"), "-G", alias).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"hostname ssh.runta.com", "user 01a1b016-11bc-7e63-b3d6-c4955e17b73b", "stricthostkeychecking true", "forwardagent no"} {
		if !strings.Contains(string(output), line) {
			t.Fatal(line)
		}
	}
}
func TestRejectSSHConfigInjection(t *testing.T) {
	if _, err := quoteConfig("/tmp/key\nProxyCommand bad"); err == nil {
		t.Fatal("newline accepted")
	}
	if _, err := quoteConfig("/tmp/%h"); err == nil {
		t.Fatal("SSH expansion accepted")
	}
}

func TestPrivateSSHConfigurationForSelfHostedTarget(t *testing.T) {
	root := t.TempDir()
	identity := filepath.Join(root, "identity")
	os.MkdirAll(identity, 0700)
	os.WriteFile(filepath.Join(identity, "runtime_key"), []byte("unused"), 0600)
	os.WriteFile(filepath.Join(identity, "known_hosts"), nil, 0600)
	dir := filepath.Join(root, "wrapper")
	target := cloudtarget.Target{Dir: identity, Host: "vps.example.com", Port: 2222, User: "fennec"}
	alias, err := ConfigureTargetSSH(dir, "0b9f6c1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f", target)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(filepath.Join(dir, "ssh"), "-G", alias).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"hostname vps.example.com", "port 2222", "user fennec", "stricthostkeychecking true"} {
		if !strings.Contains(string(output), line) {
			t.Fatal(line)
		}
	}
	for _, bad := range []cloudtarget.Target{{Dir: identity, Host: "a b", Port: 22, User: "fennec"}, {Dir: identity, Host: "h", Port: 70000, User: "fennec"}, {Dir: identity, Host: "h", Port: 22, User: "x\nProxyCommand y"}} {
		if _, err := ConfigureTargetSSH(dir, "0b9f6c1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f", bad); err == nil {
			t.Fatalf("accepted target %+v", bad)
		}
	}
}
