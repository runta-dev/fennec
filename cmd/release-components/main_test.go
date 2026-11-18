package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fennec/internal/updater"
)

func manifest(tree, key string) *updater.ComponentSet {
	return &updater.ComponentSet{Format: 1, Components: []updater.Component{
		{Name: "core"},
		{Name: "chromium", Path: updater.ChromiumComponentPath, Tree: tree, Key: key},
	}}
}

func TestComparePreviousRequiresStableChromiumForUnchangedInputs(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	prev, _ := json.Marshal(manifest(a, "key-1"))
	if note, err := comparePrevious(prev, manifest(a, "key-1")); err != nil || !strings.Contains(note, "reuse") {
		t.Fatalf("unchanged Chromium: %q %v", note, err)
	}
	if _, err := comparePrevious(prev, manifest(b, "key-1")); err == nil {
		t.Fatal("re-signed Chromium with unchanged inputs accepted")
	}
	if note, err := comparePrevious(prev, manifest(b, "key-2")); err != nil || !strings.Contains(note, "changed") {
		t.Fatalf("new pin: %q %v", note, err)
	}
	empty, _ := json.Marshal(&updater.ComponentSet{Format: 1})
	if note, err := comparePrevious(empty, manifest(a, "key-1")); err != nil || !strings.Contains(note, "new") {
		t.Fatalf("first component release: %q %v", note, err)
	}
}

// Fennec.app no longer bundles Chromium: its manifest lists no component and
// still verifies, so packaging produces a core-only release.
func TestBundleWithoutChromiumHasNoComponents(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Fennec.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents/MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, err := bundleManifest(app, "")
	if err != nil || len(set.Components) != 0 {
		t.Fatalf("manifest %+v %v", set, err)
	}
	raw, _ := json.Marshal(set)
	path := filepath.Join(app, filepath.FromSlash(updater.BundleComponentsPath))
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(app); err != nil {
		t.Fatalf("verify: %v", err)
	}
}
