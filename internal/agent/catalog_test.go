package agent

import (
	"os"
	"path/filepath"
	"strings"
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
	// ~/.kimi is the legacy Python kimi-cli, another product: not evidence.
	os.WriteFile(filepath.Join(home, ".kimi/config.toml"), []byte(""), 0600)
	if found = discoverAt(home, []string{bin}, nil); len(found) != 1 {
		t.Fatalf("legacy kimi-cli taken for Kimi Code: %+v", found)
	}
	os.MkdirAll(filepath.Join(home, ".kimi-code"), 0700)
	os.WriteFile(filepath.Join(home, ".kimi-code/config.toml"), []byte(""), 0600)
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

func TestPanelListOrdersCodexDesktopFirst(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "ChatGPT.app/Contents/Resources/codex-cli"), 0700)
	panel := panelList(nil, []string{root})
	order := []string{}
	for _, entry := range panel[:5] {
		order = append(order, entry.ID)
	}
	if strings.Join(order, ",") != "codex-desktop,codex,claude,pi,kimi" || !panel[0].Installed || !panel[0].Cloud {
		t.Fatal(order, panel[0])
	}
	if absent := panelList(nil, []string{t.TempDir()}); absent[0].ID != CodexDesktopID || absent[0].Installed {
		t.Fatal(absent[0])
	}
	for _, entry := range PanelCatalog(nil) {
		if entry.ID == CodexDesktopID {
			t.Fatal("desktop application leaked into runtime catalog")
		}
	}
}
