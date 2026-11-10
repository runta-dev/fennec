package syncengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectMappingIsolationAndSubdirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "src", "nested")
	os.MkdirAll(sub, 0700)
	a, err := MapProject("alice", "mac-one", root, sub, "/home/user")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MapProject("alice", "mac-two", root, sub, "/home/user")
	c, _ := MapProject("bob", "mac-one", root, sub, "/home/user")
	if a.ID == b.ID || a.ID == c.ID || !strings.HasSuffix(a.CloudDirectory, "/src/nested") || strings.Contains(a.CloudDirectory, root) {
		t.Fatalf("bad scope %+v", a)
	}
	repeat, _ := MapProject("alice", "mac-one", root, root, "/home/user")
	if a.ID != repeat.ID {
		t.Fatal("cwd changed project identity")
	}
}
func TestMappingRejectsEscapingCwd(t *testing.T) {
	if _, err := MapProject("u", "d", t.TempDir(), t.TempDir(), "/root"); err == nil {
		t.Fatal("escaping cwd accepted")
	}
}
