package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelIdentityReconcilePreservesExistingLogin(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0700)
	auth := filepath.Join(home, ".codex/auth.json")
	os.WriteFile(auth, []byte(`{"existing":true}`), 0600)
	cmd := exec.Command("sh", "-s")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(RestoreCodexIdentityScript)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	data, _ := os.ReadFile(auth)
	if string(data) != `{"existing":true}` {
		t.Fatal("overwrote existing identity")
	}
}
