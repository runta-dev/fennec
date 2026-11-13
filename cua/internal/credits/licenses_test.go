package credits

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Simplified templates with the placeholders licenses.py fills.
const (
	testFileTmpl  = "<html>{{reciprocal-license-statement}}<div>\n{{entries}}\n</div>\n"
	testEntryTmpl = "<div class=\"product\" id=\"{{id}}\"><span class=\"title\">{{name}}</span><a href=\"{{url}}\">h</a><pre>{{license}}</pre></div>"
	testRecipTmpl = "<p>{{opensource_project}} at {{opensource_link}}</p>"
)

func readme(name, url, license, file, shipped string) string {
	var b strings.Builder
	for _, kv := range [][2]string{{"Name", name}, {"URL", url}, {"License", license}, {"License File", file}, {"Shipped", shipped}} {
		if kv[1] != "" {
			b.WriteString(kv[0] + ": " + kv[1] + "\n")
		}
	}
	return b.String()
}

// fixture builds a small checkout exercising licenses.py's rules.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"LICENSE":          "Chromium licence\r\nline two\r\n",
		fileTemplate:       testFileTmpl,
		entryTemplate:      testEntryTmpl,
		reciprocalTemplate: testRecipTmpl,
		"base/keep.txt":    "x",
		"third_party/foo/README.chromium": readme("Foo", "https://foo.test", "MIT", "LICENSE", "yes") +
			"Version: 1\nDescription: ignored: fields\n",
		"third_party/foo/LICENSE":                                "foo licence <&>\n",
		"third_party/foo/src/third_party/nested/README.chromium": readme("Nested", "https://n.test", "BSD-3-Clause", "", ""),
		"third_party/foo/src/third_party/nested/COPYING":         "nested copying\n",
		"third_party/notshipped/README.chromium":                 readme("Not Shipped", "https://ns.test", "MIT", "LICENSE", "no"),
		"third_party/notshipped/LICENSE":                         "never shown\n",
		"third_party/legacy/README.chromium":                     readme("Legacy", "https://l.test", "MIT", "NOT_SHIPPED", ""),
		"third_party/capital/README.chromium":                    readme("Capital", "https://c.test", "MIT", "L", "No"),
		"third_party/capital/L":                                  "capital licence\n",
		"third_party/multi/README.chromium": readme("Multi A", "https://a.test", "MIT", "/third_party/foo/LICENSE, missing.txt", "yes") +
			"-------------------- DEPENDENCY DIVIDER --------------------\n" +
			readme("Multi B", "https://b.test", "MIT", "nothere", "yes") +
			"-------------------- DEPENDENCY DIVIDER --------------------\n" +
			readme("Multi C", "https://c.test", "MIT", "../../../../etc/passwd", "yes"),
		"third_party/dupe/README.chromium":                             "Name: Foo\nName: Again\n",
		"third_party/noreadme/code.c":                                  "int x;",
		"third_party/same1/README.chromium":                            readme("Same", "https://s.test", "MIT", "LICENSE", "yes"),
		"third_party/same1/LICENSE":                                    "same licence\n",
		"third_party/same2/README.chromium":                            readme("SAME", "https://s.test", "MIT", "LICENSE", "yes"),
		"third_party/same2/LICENSE":                                    "same licence\n",
		"third_party/same3/README.chromium":                            readme("same", "https://s.test", "MIT", "LICENSE", "yes"),
		"third_party/same3/LICENSE":                                    "a different licence\n",
		"third_party/blink/LICENSE_FOR_ABOUT_CREDITS":                  "webkit licence\n",
		"third_party/blink/renderer/x.cc":                              "x",
		"third_party/llvm/README.chromium":                             readme("LLVM", "https://llvm.test", "Apache-2.0", "LICENSE", "yes"),
		"third_party/llvm/LICENSE":                                     "pruned\n",
		"third_party/withjson/README.chromium":                         readme("With JSON", "https://j.test", "MIT", "LICENSE", "yes"),
		"third_party/withjson/LICENSE":                                 "json licence\n",
		"third_party/withjson/" + additionalPathsFile:                  `["sub/a", "../../../outside"]`,
		"third_party/withjson/sub/a/README.chromium":                   readme("Sub A", "https://sa.test", "MIT", "LICENSE", "yes"),
		"third_party/withjson/sub/a/LICENSE":                           "sub a licence\n",
		"third_party/rust/serde/v1/README.chromium":                    readme("serde", "https://serde.test", "MIT", "LICENSE", "yes"),
		"third_party/rust/serde/v1/LICENSE":                            "serde licence\n",
		"third_party/rust/chromium_crates_io/vendor/x/README.chromium": readme("Vendored", "https://v.test", "MIT", "LICENSE", "yes"),
		"third_party/rust/chromium_crates_io/vendor/x/LICENSE":         "vendored\n",
		"third_party/escape/README.chromium":                           readme("Escape <\"'&>", "https://e.test/?a=1&b=2", "MIT", "LICENSE", "yes"),
		"third_party/escape/LICENSE":                                   "bad bytes \xff\xe2\x82 end {{url}}\n",
		"third_party/linked/README.chromium":                           readme("Linked", "https://ln.test", "MIT", "LICENSE", "yes"),
		"deep/third_party/zzz/README.chromium":                         readme("Deep", "https://d.test", "MIT", "LICENSE", "yes"),
		"deep/third_party/zzz/LICENSE":                                 "deep licence\n",
		// licenses.py prunes every top-level path starting with "out".
		"outside/third_party/zzz/README.chromium":     readme("Outside", "https://o.test", "MIT", "LICENSE", "yes"),
		"outside/third_party/zzz/LICENSE":             "outside licence\n",
		"out/Release/third_party/gen/README.chromium": readme("Generated", "https://g.test", "MIT", "LICENSE", "yes"),
		"out/Release/third_party/gen/LICENSE":         "built\n",
		"v8/LICENSE":                                  "v8 licence\n",
		"v8/src/x.cc":                                 "x",
	}
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A submodule that is not checked out: an empty directory.
	if err := os.MkdirAll(filepath.Join(root, "third_party", "emptysub", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../foo/LICENSE", filepath.Join(root, "third_party", "linked", "LICENSE")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindThirdPartyDirs(t *testing.T) {
	dirs, err := FindThirdPartyDirs(DirTree(fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"deep/third_party/zzz",
		"third_party/blink", "third_party/capital", "third_party/dupe", "third_party/escape", "third_party/foo",
		"third_party/foo/src/third_party/nested", "third_party/legacy", "third_party/linked", "third_party/multi",
		"third_party/noreadme", "third_party/notshipped", "third_party/rust", "third_party/rust/serde/v1",
		"third_party/same1", "third_party/same2", "third_party/same3", "third_party/withjson", "third_party/withjson/sub/a",
		"v8",
	}
	if !reflect.DeepEqual(dirs, want) {
		t.Errorf("dirs\n got %q\nwant %q", dirs, want)
	}
}

func TestParseMetadataFile(t *testing.T) {
	deps, err := parseMetadataFile("Name: A\r\nURL: u\nlicense file: x ,  y\nShipped in Chromium: yes\nUnknown: z\nNo colon\n-------------------- DEPENDENCY DIVIDER --------------------\n  Name: B  \n", "f")
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"Name": "A", "URL": "u", "License File": []string{"x", "y"}, "Shipped in Chromium": "yes"},
		{"Name": "B"},
	}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("got %v", deps)
	}
	if _, err := parseMetadataFile("Name: A\nName: B\n", "f"); err == nil {
		t.Error("a duplicate field passed")
	}
}

func TestGenerate(t *testing.T) {
	root := fixture(t)
	out, err := Generate(DirTree(root))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range out.Scan.Credited {
		names = append(names, m.Name+"@"+m.Dir)
	}
	want := []string{
		"The Chromium Project@", "Capital@third_party/capital", "Deep@deep/third_party/zzz",
		"Escape <\"'&>@third_party/escape", "Foo@third_party/foo", "Linked@third_party/linked",
		"Multi A@third_party/multi", "Nested@third_party/foo/src/third_party/nested",
		"Same@third_party/same1", "SAME@third_party/same2", "same@third_party/same3", "serde@third_party/rust/serde/v1",
		"Sub A@third_party/withjson/sub/a", "V8 JavaScript Engine@v8", "WebKit@third_party/blink", "With JSON@third_party/withjson",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("credited\n got %q\nwant %q", names, want)
	}
	byName := map[string]Metadata{}
	for _, m := range out.Scan.Credited {
		byName[m.Name] = m
	}
	if f := byName["Multi A"].LicenseFiles; !reflect.DeepEqual(f, []string{"third_party/foo/LICENSE"}) {
		t.Errorf("Multi A licence files %v", f)
	}
	if f := byName["Nested"].LicenseFiles; !reflect.DeepEqual(f, []string{"third_party/foo/src/third_party/nested/COPYING"}) {
		t.Errorf("COPYING fallback %v", f)
	}
	skipped := map[string]bool{}
	for _, p := range out.Scan.Skipped {
		skipped[p.Dir] = true
	}
	for _, d := range []string{"third_party/multi", "third_party/dupe", "third_party/noreadme", "third_party/rust"} {
		if !skipped[d] {
			t.Errorf("%s is not reported", d)
		}
	}
	html := string(out.HTML)
	if !strings.HasPrefix(html, "<!-- Generated by licenses.py; do not edit. --><html><p>Chromium at https://source.chromium.org/chromium</p><div>\n") {
		t.Errorf("head %q", html[:min(200, len(html))])
	}
	// Same name and licence merge (case-insensitively); a different
	// licence under the same name stays.
	if n := strings.Count(html, `<span class="title">Same</span>`) + strings.Count(html, `<span class="title">SAME</span>`); n != 1 {
		t.Errorf("Same/SAME shown %d times", n)
	}
	if !strings.Contains(html, `<span class="title">same</span>`) || out.Components != 15 {
		t.Errorf("components %d", out.Components)
	}
	for _, want := range []string{
		`<div class="product" id="0"><span class="title">Capital</span>`,
		`<span class="title">Escape &lt;&quot;&#x27;&amp;&gt;</span><a href="https://e.test/?a=1&amp;b=2">`,
		"<pre>bad bytes �� end {{url}}\n</pre>",
		"<pre>foo licence &lt;&amp;&gt;\n</pre>",
		"<pre>Chromium licence\nline two\n</pre>",
		"<pre>webkit licence\n</pre>",
		"<pre>v8 licence\n</pre>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, never := range []string{"never shown", "outside licence", "pruned", "built", "vendored", "capital licence\n\n"} {
		if strings.Contains(html, never) {
			t.Errorf("%q is credited", never)
		}
	}
	text := string(out.Text)
	if !strings.HasPrefix(text, "--------------------\nThe Chromium Project\n--------------------\nChromium licence\nline two\n\n--------------------\nCapital\n") {
		t.Errorf("text %q", text[:min(200, len(text))])
	}
	// Deterministic, and sensitive to content.
	again, err := Generate(DirTree(root))
	if err != nil || again.SHA256() != out.SHA256() {
		t.Errorf("a second run differs: %v", err)
	}
	_ = os.WriteFile(filepath.Join(root, "third_party/foo/LICENSE"), []byte("changed\n"), 0o644)
	if changed, _ := Generate(DirTree(root)); changed.SHA256() == out.SHA256() {
		t.Error("a changed licence keeps the hash")
	}
}

func TestRulesCheck(t *testing.T) {
	root := fixture(t)
	p := filepath.Join(root, filepath.FromSlash(RulesPath))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("# another licenses.py\n"), 0o644)
	if _, err := Generate(DirTree(root)); err == nil || !strings.Contains(err.Error(), RulesBlob) {
		t.Errorf("a different licenses.py was accepted: %v", err)
	}
	if gitBlobID([]byte("hello\n")) != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Error("git blob id")
	}
}

func TestDecodeUTF8Replace(t *testing.T) {
	for in, want := range map[string]string{
		"ok é":              "ok é",
		"\xff":              "�",
		"\xe2\x82":          "�",
		"\xe2\x82x":         "�x",
		"\xf0\x9f\x98":      "�",
		"\xed\xa0\x80":      "���", // a surrogate
		"\xc0\xaf":          "��",
		"a\xf4\x90\x80\x80": "a����",
	} {
		if got := decodeUTF8Replace([]byte(in)); got != want {
			t.Errorf("%q = %q; want %q", in, got, want)
		}
	}
	if got := pySplitlines("a\r\nb\rc\nd\x0be f\n"); !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Errorf("splitlines %q", got)
	}
}

func TestResolve(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "a", "b", "f"), []byte("x"), 0o644)
	_ = os.Symlink("a/b", filepath.Join(root, "l"))
	_ = os.Symlink("../l/f", filepath.Join(root, "a", "lf"))
	_ = os.Symlink("/etc", filepath.Join(root, "abs"))
	_ = os.Symlink("loop", filepath.Join(root, "loop"))
	tr := DirTree(root)
	for p, want := range map[string]string{"l/f": "a/b/f", "a/lf": "a/b/f", "abs": "", "a/b/f/x": "", "../x": ""} {
		got, k, _ := resolve(tr, p)
		if want == "" && k != KindNone || want != "" && (got != want || k != KindFile) {
			t.Errorf("%s -> %q %v", p, got, k)
		}
	}
	if _, _, err := resolve(tr, "loop"); err == nil {
		t.Error("a link loop resolved")
	}
}
