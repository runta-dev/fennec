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

func TestComingSoonCatalogDoesNotDependOnInstallation(t *testing.T) {
	expected := []string{"claude", "codex", "pi", "kimi", "claude-desktop", "opencode", "devin", "grok", "cursor"}
	empty := PanelCatalog(nil)
	if len(empty) != len(expected) {
		t.Fatal(empty)
	}
	for i, id := range expected {
		if empty[i].ID != id || empty[i].Installed || empty[i].Evidence != "" {
			t.Fatal("placeholder claims installation", empty[i])
		}
	}
	present := PanelCatalog([]Agent{{ID: "codex", Installed: true, Name: "Codex", Evidence: "executable"}, {ID: "kimi", Installed: true, Name: "Kimi Code", Evidence: "configuration"}})
	if len(present) != 9 || present[1].ID != "codex" || !present[1].Installed || !present[3].Installed || present[3].ID != "kimi" {
		t.Fatal("missing or duplicate catalog entries", present)
	}
	if got := discoverAt(t.TempDir(), nil, nil); len(got) != 0 {
		t.Fatal("placeholders contaminated real discovery", got)
	}
}

func TestCleanMacKeepsCloudAgentEntryPoints(t *testing.T) {
	home := t.TempDir()
	discovered := discoverAt(home, nil, nil)
	if len(discovered) != 0 {
		t.Fatal("clean fixture contains installed agents")
	}
	panel := PanelCatalog(discovered)
	byID := map[string]Agent{}
	for _, entry := range panel {
		byID[entry.ID] = entry
	}
	for _, id := range CloudAgentIDs() {
		entry, ok := byID[id]
		if !ok || !entry.Cloud || entry.Installed || entry.Evidence != "" {
			t.Fatalf("cloud entry %s unavailable or pretends to be installed: %+v", id, entry)
		}
	}
}
