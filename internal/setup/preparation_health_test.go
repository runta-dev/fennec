package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResidentPreparationHealthChecksLiveServicesAndScope(t *testing.T) {
	home := t.TempDir()
	write := func(name, value string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"node", "herdr-core", "codex", "curl", "magpie"} {
		write(".local/bin/"+name, "#!/bin/sh\nexit 0\n", 0700)
	}
	write(".local/fennec/bin/fennec-terminal", "#!/bin/sh\nexit 0\n", 0700)
	write(".local/fennec/gateway/fennec-terminal", "#!/bin/sh\nexit 0\n", 0700)
	health := PreparationHealth{Fingerprint: strings.Repeat("a", 64), Marker: "prepared-runtime", Agents: []string{"codex"}, Magpie: true}
	if CheckPreparationHealth(context.Background(), home, health) == nil {
		t.Fatal("missing marker accepted")
	}
	write(".config/fennec/prepared-runtime", health.Fingerprint, 0600)
	if err := CheckPreparationHealth(context.Background(), home, health); err != nil {
		t.Fatal(err)
	}
	health.Fingerprint = strings.Repeat("b", 64)
	if CheckPreparationHealth(context.Background(), home, health) == nil {
		t.Fatal("wrong scope accepted")
	}
	health.Fingerprint = strings.Repeat("a", 64)
	write(".local/bin/herdr-core", "#!/bin/sh\nexit 1\n", 0700)
	if CheckPreparationHealth(context.Background(), home, health) == nil {
		t.Fatal("dead session service accepted")
	}
	write(".local/bin/herdr-core", "#!/bin/sh\nexit 0\n", 0700)
	write(".local/bin/curl", "#!/bin/sh\nexit 1\n", 0700)
	if CheckPreparationHealth(context.Background(), home, health) == nil {
		t.Fatal("dead gateway accepted")
	}
	health.Magpie = false
	if err := CheckPreparationHealth(context.Background(), home, health); err != nil {
		t.Fatal("native mode depends on gateway", err)
	}
	for _, id := range []string{"codex|pi", "codex;true", "", "../codex"} {
		health.Agents = []string{id}
		if health.Validate() == nil {
			t.Fatalf("unsafe agent accepted: %q", id)
		}
	}
}
