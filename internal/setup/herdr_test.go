package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrReconcileStartsStoppedCoreOnce(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	fake := `#!/bin/sh
case "$1 $2" in
 '--version ') echo 'hc 0.1.18';;
 'server status') test -f "$HOME/core-running";;
 'server start') echo start >> "$HOME/starts"; touch "$HOME/core-running";;
 'ls --json') test -f "$HOME/core-running";;
 *) exit 97;;
esac
`
	os.WriteFile(filepath.Join(bin, "herdr-core"), []byte(fake), 0700)
	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", "-s")
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(HerdrSetupScript)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, output)
		}
	}
	data, _ := os.ReadFile(filepath.Join(home, "starts"))
	if string(data) != "start\n" {
		t.Fatalf("unexpected starts: %q", data)
	}
}
func TestHerdrReconcilePreservesUnrecognizedVersion(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	path := filepath.Join(bin, "herdr-core")
	original := []byte("#!/bin/sh\necho 'hc 9.0.0'\n")
	os.WriteFile(path, original, 0700)
	cmd := exec.Command("sh", "-s")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(HerdrSetupScript)
	if cmd.Run() == nil {
		t.Fatal("unknown version replaced or accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("replaced user binary")
	}
}
