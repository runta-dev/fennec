package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestManifestUsesDirtyWorkingTreeAndExcludesSecrets(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	run("init", "-q")
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.txt", "before")
	write(".env", "secret")
	run("add", "tracked.txt", ".env")
	write("tracked.txt", "dirty")
	write("untracked.txt", "new")
	write(".gitignore", "ignored.txt\n")
	write("ignored.txt", "ignored")
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(root, "linked"))
	m, err := ManifestGit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]File{}
	for _, f := range m.Files {
		files[f.Path] = f
	}
	sum := sha256.Sum256([]byte("dirty"))
	if files["tracked.txt"].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("hashed index rather than working tree")
	}
	if _, ok := files["untracked.txt"]; !ok {
		t.Fatal("missing untracked file")
	}
	for _, name := range []string{".env", "ignored.txt", "linked"} {
		if _, ok := files[name]; ok {
			t.Fatal("included excluded file", name)
		}
	}
	if len(m.Excluded) != 2 {
		t.Fatalf("missing exclusions: %+v", m.Excluded)
	}
}
func TestRejectNonGitDirectory(t *testing.T) {
	if _, err := ManifestGit(context.Background(), t.TempDir()); err == nil {
		t.Fatal("nonproject accepted")
	}
}
