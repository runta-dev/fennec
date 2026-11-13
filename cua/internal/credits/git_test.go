package credits

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// gitRepo makes a repository serving partial clones, commits files (and
// submodule links) and answers the commit.
func gitRepo(t *testing.T, dir string, files map[string]string, links map[string]string) string {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("config", "uploadpack.allowFilter", "true")
	run("config", "uploadpack.allowAnySHA1InWant", "true")
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	for p, commit := range links {
		run("update-index", "--add", "--cacheinfo", "160000,"+commit+","+p)
	}
	run("commit", "-q", "-m", "c")
	return run("rev-parse", "HEAD")
}

func TestGitTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	base := t.TempDir()
	angleSub := gitRepo(t, filepath.Join(base, "anglesub"), map[string]string{
		"README.chromium": readme("Angle Sub", "https://as.test", "MIT", "LICENSE", "yes"), "LICENSE": "angle sub licence\n"}, nil)
	angle := gitRepo(t, filepath.Join(base, "angle"), map[string]string{
		"DEPS": "use_relative_paths = True\nvars = {'build_with_chromium': False}\ndeps = {\n" +
			"  'third_party/sub/src': { 'url': 'file://" + base + "/anglesub@" + angleSub + "', 'condition': 'build_with_chromium' },\n" +
			"  'third_party/standalone/src': { 'url': 'file://" + base + "/anglesub@" + angleSub + "', 'condition': 'not build_with_chromium' },\n}\n",
		"LICENSE":                           "angle licence\n",
		"README.chromium":                   readme("ANGLE", "https://angle.test", "BSD-3-Clause", "LICENSE", "yes"),
		"third_party/sub/README.chromium":   readme("Angle Sub", "https://as.test", "MIT", "src/LICENSE", "yes"),
		"third_party/standalone/README.txt": "x",
	}, map[string]string{"third_party/sub/src": angleSub, "third_party/standalone/src": angleSub})
	// The submodule links point at the same commit; DEPS decides which
	// are checked out.
	src := gitRepo(t, filepath.Join(base, "src"), map[string]string{
		"DEPS": "vars = {'build_with_chromium': True}\ndeps = {\n" +
			"  'src/third_party/angle': 'file://" + base + "/angle@" + angle + "',\n" +
			"  'src/third_party/linux_only': { 'url': 'file://" + base + "/anglesub@" + angleSub + "', 'condition': 'checkout_linux' },\n}\n" +
			"recursedeps = ['src/third_party/angle']\n",
		"LICENSE":                                "chromium licence\n",
		fileTemplate:                             testFileTmpl,
		entryTemplate:                            testEntryTmpl,
		reciprocalTemplate:                       testRecipTmpl,
		"third_party/linux_only/README.chromium": readme("Linux Only", "https://lo.test", "MIT", "LICENSE", "yes"),
		"third_party/own/README.chromium":        readme("Own", "https://own.test", "MIT", "LICENSE", "yes"),
		"third_party/own/LICENSE":                "own licence\n",
	}, map[string]string{"third_party/angle": angle, "third_party/linux_only/src": angleSub})

	var logs []string
	f := &Fetcher{Cache: filepath.Join(base, "cache"), SrcURL: "file://" + base + "/src", allowFile: true,
		Log: func(format string, args ...any) { logs = append(logs, format) }}
	tree, err := f.Tree(context.Background(), src, MacPlatform("arm64"))
	if err != nil {
		t.Fatal(err)
	}
	var deps []string
	for _, d := range tree.Deps {
		deps = append(deps, d.Path)
	}
	if !reflect.DeepEqual(deps, []string{"third_party/angle", "third_party/angle/third_party/sub/src"}) {
		t.Errorf("deps %v", deps)
	}
	if k := tree.Lstat("third_party/linux_only/src"); k != KindDir {
		t.Errorf("an unchecked-out submodule is %v", k)
	}
	if es, _ := tree.ReadDir("third_party/linux_only/src"); len(es) != 0 {
		t.Errorf("an unchecked-out submodule holds %v", es)
	}
	out, err := Generate(tree)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range out.Scan.Credited {
		names = append(names, m.Name+"@"+m.Dir)
	}
	want := []string{"The Chromium Project@", "ANGLE@third_party/angle", "Angle Sub@third_party/angle/third_party/sub", "Own@third_party/own"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("credited %q", names)
	}
	if !strings.Contains(string(out.HTML), "<pre>angle sub licence\n</pre>") {
		t.Error("the licence inside the recursed dependency is missing")
	}
	// A second fetcher on the same cache reads everything locally.
	f2 := &Fetcher{Cache: f.Cache, SrcURL: f.SrcURL, allowFile: true}
	t2, err := f2.Tree(context.Background(), src, MacPlatform("arm64"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Generate(t2)
	if err != nil || again.SHA256() != out.SHA256() {
		t.Errorf("the cached run differs: %v", err)
	}
	if _, err := (&Fetcher{Cache: f.Cache}).repo("http://insecure.test/x"); err == nil {
		t.Error("a non-https repository was accepted")
	}
}
