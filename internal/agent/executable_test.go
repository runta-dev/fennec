package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryExcludesOwnShimsAndCopies(t *testing.T) {
	home := t.TempDir()
	shim := filepath.Join(home, ".local/share/fennec/shims")
	os.MkdirAll(shim, 0700)
	script := []byte("#!/bin/sh\n# Fennec managed launcher v1\nexit 99\n")
	path := filepath.Join(shim, "codex")
	os.WriteFile(path, script, 0700)
	copy := filepath.Join(home, "copied")
	os.WriteFile(copy, script, 0700)
	alias := filepath.Join(home, "alias")
	os.Symlink(path, alias)
	for _, p := range []string{path, copy, alias} {
		if originalExecutable(home, p) {
			t.Fatal("launcher accepted", p)
		}
	}
	found := discoverAt(home, []string{shim}, nil)
	if len(found) != 0 {
		t.Fatal("shim advertised as installed agent", found)
	}
	original := filepath.Join(home, "real")
	os.WriteFile(original, []byte("#!/bin/sh\necho 1.0.0\n"), 0700)
	if !originalExecutable(home, original) {
		t.Fatal("real CLI rejected")
	}
}
