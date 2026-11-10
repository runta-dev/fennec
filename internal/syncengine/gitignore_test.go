package syncengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProjectIgnorePatterns(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# generated\nartifacts/\n*.tmp\nbuild/output/\n/root-only\n"), 0600)
	got, err := ProjectIgnores(root)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"artifacts/", "*.tmp", "/build/output/", "/root-only"}, defaultIgnores...)
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	for _, pattern := range []string{"!keep.txt\n", "escaped\\ name\n"} {
		os.WriteFile(filepath.Join(root, ".gitignore"), []byte(pattern), 0600)
		if _, err := ProjectIgnores(root); err == nil {
			t.Fatal("unverified semantics accepted")
		}
	}
}

func TestNestedIgnoreScope(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "src"), 0700)
	os.WriteFile(filepath.Join(root, "src/.gitignore"), []byte("*.tmp\n/cache/\nbuild/output/\n"), 0600)
	rules, err := ProjectIgnores(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/src/**/*.tmp", "/src/cache/", "/src/build/output/"}
	if !reflect.DeepEqual(rules[:3], want) {
		t.Fatal(rules)
	}
}
