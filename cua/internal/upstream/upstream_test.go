package upstream

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryClassifiesPlatforms(t *testing.T) {
	root := t.TempDir()
	write(t, root, "runtime/test/a.test.mts", `
test('shared', async () => {})
test('mac only', { skip: process.platform !== 'darwin' }, async () => {})
test('not on mac', { skip: process.platform === 'darwin' }, async () => {})
test('linux only', { skip: process.platform !== 'linux' }, async () => {})
test('it\'s quoted', async () => {})
`)
	write(t, root, "crates/desk/src/desk.rs", `
#[test]
fn shared() {}

#[cfg(windows)]
#[test]
fn windows_only() {}

#[tokio::test(start_paused = true)]
async fn paused() {}
`)
	write(t, root, "crates/desk/src/atspi.rs", "#[test]\nfn tree() {}\n")
	write(t, root, "crates/android/src/adb.rs", "#[test]\nfn adb() {}\n")
	write(t, root, "CHANGELOG.md", `# Changelog

## 0.1.1 - 2026-01-02

- **macOS: a fix.** Details.
  - a sub-bullet
- **Linux: another.** Details.

## 0.1.0 - 2026-01-01

- Plain note without bold.
`)
	items, err := Inventory(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"node/a/shared":                       true,
		"node/a/mac only":                     true,
		"node/a/not on mac":                   false,
		"node/a/linux only":                   false,
		"node/a/it's quoted":                  true,
		"rust/desk/src/desk.rs::shared":       true,
		"rust/desk/src/desk.rs::windows_only": false,
		"rust/desk/src/desk.rs::paused":       true,
		"rust/desk/src/atspi.rs::tree":        false,
		"rust/android/src/adb.rs::adb":        false,
		"changelog/0.1.1/1":                   true,
		"changelog/0.1.1/2":                   false,
		"changelog/0.1.0/1":                   true,
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.ID] = it.MacOS
	}
	if len(got) != len(want) {
		t.Errorf("got %d items, want %d: %v", len(got), len(want), got)
	}
	for id, mac := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("missing %s", id)
		} else if g != mac {
			t.Errorf("%s: macOS = %v, want %v", id, g, mac)
		}
	}
}

func TestReadPin(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "UPSTREAM", "# c\nrepo https://example.com/x\ncommit 0123456789abcdef\nversion 1.0.0\n")
	p, err := ReadPin(filepath.Join(dir, "UPSTREAM"))
	if err != nil || p.Repo != "https://example.com/x" || p.Commit != "0123456789abcdef" || p.Version != "1.0.0" {
		t.Fatalf("%+v %v", p, err)
	}
}
