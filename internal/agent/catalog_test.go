package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryEvidence(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0700)
	os.WriteFile(filepath.Join(bin, "pi"), []byte("test"), 0700)
	os.WriteFile(filepath.Join(bin, "grok"), []byte("unrelated"), 0700)
	os.MkdirAll(filepath.Join(home, ".kimi", "skills"), 0700)
	found := discoverAt(home, []string{bin}, nil)
	if len(found) != 1 || found[0].ID != "pi" {
		t.Fatalf("false positives: %+v", found)
	}
	os.WriteFile(filepath.Join(home, ".kimi/config.toml"), []byte(""), 0600)
	found = discoverAt(home, []string{bin}, nil)
	if len(found) != 2 || found[1].ID != "kimi" {
		t.Fatalf("missing config evidence: %+v", found)
	}
}
