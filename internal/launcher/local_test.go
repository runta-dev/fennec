package launcher

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDesktopAgentColorEnvironment(t *testing.T) {
	cases := []struct {
		name, session            string
		interactive, wantNoColor bool
	}{
		{"desktop", "fennec", true, false},
		{"desktop pipe", "fennec", false, true},
		{"other terminal", "herdr", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := []string{"HC_SESSION=" + tc.session, "NO_COLOR=1", "COLORTERM=truecolor", "FENNEC_LAUNCH_MODE=cloud", DispatchedEnv + "=old", "PATH=/test/bin"}
			result := localEnvironment(source, tc.interactive)
			got := map[string]string{}
			for _, v := range result {
				k, val, _ := strings.Cut(v, "=")
				got[k] = val
			}
			_, hasNoColor := got["NO_COLOR"]
			if hasNoColor != tc.wantNoColor {
				t.Fatalf("NO_COLOR present=%v; want %v", hasNoColor, tc.wantNoColor)
			}
			if got[DispatchedEnv] != "1" || got["PATH"] != "/test/bin" || got["COLORTERM"] != "truecolor" {
				t.Fatalf("agent environment changed: %v", got)
			}
			if _, ok := got["FENNEC_LAUNCH_MODE"]; ok {
				t.Fatal("inherited launch mode retained")
			}
		})
	}
}
