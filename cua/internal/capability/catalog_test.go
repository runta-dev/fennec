package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/upstream"
)

var items = []upstream.Item{
	{ID: "node/a/one", Kind: upstream.KindNode, MacOS: true},
	{ID: "node/a/two", Kind: upstream.KindNode, MacOS: true},
	{ID: "rust/x.rs::linux_only", Kind: upstream.KindRust, MacOS: false, Why: "file serves linux"},
	{ID: "changelog/0.1.0/1", Kind: upstream.KindChangelog, MacOS: true},
}

func load(t *testing.T, files map[string]string) Catalog {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const complete = `domain: demo
summary: A demo domain.
capabilities:
  - id: CAP-DEM-001
    title: One
    behavior: Does one thing.
    surface: observe
    parity: oracle
    tests: [planned:internal/view.TestOne]
    upstream: [node/a/one, node/a/two]
excluded:
  - upstream: [changelog/0.1.0/1]
    reason: Docs only.
`

func TestCompleteCatalog(t *testing.T) {
	r := Check(load(t, map[string]string{"demo.yaml": complete}), items)
	if !r.OK() {
		t.Fatalf("problems %v missing %v", r.Problems, r.Missing)
	}
	if r.Covered != 2 || r.Excluded != 1 || r.Planned != 1 {
		t.Errorf("report %+v", r)
	}
}

func TestMissingAndBadReferences(t *testing.T) {
	body := strings.Replace(complete, "[node/a/one, node/a/two]", "[node/a/one, rust/x.rs::linux_only, node/nope]", 1)
	r := Check(load(t, map[string]string{"demo.yaml": body}), items)
	if len(r.Missing) != 1 || r.Missing[0].ID != "node/a/two" {
		t.Errorf("missing %v", r.Missing)
	}
	joined := strings.Join(r.Problems, "\n")
	for _, want := range []string{"not a macOS item", "unknown upstream id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in %s", want, joined)
		}
	}
}

func TestSchemaErrors(t *testing.T) {
	body := strings.NewReplacer("CAP-DEM-001", "CAP-1", "surface: observe", "surface: moon", "parity: oracle", "parity: maybe", "Docs only.", "\"\"").Replace(complete)
	r := Check(load(t, map[string]string{"demo.yaml": body}), items)
	joined := strings.Join(r.Problems, "\n")
	for _, want := range []string{"id must look like", "unknown surface", "parity must be", "reason is required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in %s", want, joined)
		}
	}
}

func TestUnknownFieldFailsLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte(complete+"extra: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestCheckTestsFindsMissingFunctions(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "internal", "view")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "a_test.go"), []byte("package view\nfunc TestHere(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Catalog{Files: []File{{Capabilities: []Capability{{ID: "CAP-X-001", Tests: []string{"internal/view.TestHere", "internal/view.TestGone", "planned:internal/view.TestLater"}}}}}}
	got := CheckTests(c, root)
	if len(got) != 1 || !strings.Contains(got[0], "TestGone") {
		t.Errorf("problems %v", got)
	}
}
