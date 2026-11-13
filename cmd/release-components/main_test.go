package main

import (
	"encoding/json"
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
