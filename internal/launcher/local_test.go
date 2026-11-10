package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOriginalSkipsShimSymlinkAndHardlink(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "shim")
	aliases := filepath.Join(root, "aliases")
	real := filepath.Join(root, "real")
	for _, dir := range []string{shim, aliases, real} {
		os.MkdirAll(dir, 0700)
	}
	self := filepath.Join(root, "fennec")
	os.WriteFile(self, []byte("binary"), 0700)
	os.Symlink(self, filepath.Join(shim, "codex"))
	os.Link(self, filepath.Join(aliases, "codex"))
	os.WriteFile(filepath.Join(real, "codex"), []byte("original"), 0700)
	got, err := ResolveOriginal("codex", shim+string(os.PathListSeparator)+aliases+string(os.PathListSeparator)+real, shim, self)
	if err != nil || got != filepath.Join(real, "codex") {
		t.Fatalf("%s %v", got, err)
	}
}
func TestResolverFailsClosedWithoutOriginal(t *testing.T) {
	if _, err := ResolveOriginal("codex", ".:relative", "/shims", "/self"); err == nil {
		t.Fatal("relative PATH accepted")
	}
	if _, err := ResolveOriginal("bash", "/bin", "/shims", "/self"); err == nil {
		t.Fatal("unsupported CLI accepted")
	}
}
