package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadConfiguredAgyMagpieModel(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini/antigravity-cli")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, ok := readConfiguredMagpieModel(home, "agy"); ok {
		t.Fatal("model read without Magpie's settings file")
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "magpie-settings.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"model":"gemini-3-pro"}`)
	if _, ok := readConfiguredMagpieModel(home, "agy"); ok {
		t.Fatal("agy's own model read as a Magpie route")
	}
	write(`{"modelProvider":"gemini","model":"magpie/codex/gpt-6.1-sol"}`)
	if model, ok := readConfiguredMagpieModel(home, "agy"); !ok || model != "codex/gpt-6.1-sol" {
		t.Fatal(model, ok)
	}
}
