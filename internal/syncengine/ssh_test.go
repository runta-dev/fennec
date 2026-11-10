package syncengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateSSHConfiguration(t *testing.T) {
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
