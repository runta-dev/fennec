package cloudimage

import (
	"os"
	"path/filepath"
	"testing"
)

func seedLayout(t *testing.T) Layout {
	t.Helper()
	l := testLayout(t)
	l.Seed = filepath.Join(t.TempDir(), "seed")
	node := filepath.Join(l.Seed, "node")
	for _, dir := range []string{"bin", "lib/node_modules/npm/bin"} {
		if err := os.MkdirAll(filepath.Join(node, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(node, "bin/node"), []byte("#!/bin/sh\necho v22.22.0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(node, "lib/node_modules/npm/bin/npm-cli.js"), []byte("cli"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lib/node_modules/npm/bin/npm-cli.js", filepath.Join(node, "bin/npm")); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestInitSeedsNodeOnce(t *testing.T) {
	l := seedLayout(t)
	if _, err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(l.Home, ".local/fennec/node")
	info, err := os.Stat(filepath.Join(node, "bin/node"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("node not seeded executable: %v %v", info, err)
	}
	if link, err := os.Readlink(filepath.Join(node, "bin/npm")); err != nil || link != "../lib/node_modules/npm/bin/npm-cli.js" {
		t.Fatalf("npm symlink not preserved: %q %v", link, err)
	}
	if data, err := os.ReadFile(filepath.Join(node, "bin/npm")); err != nil || string(data) != "cli" {
		t.Fatal("npm symlink does not resolve inside the seeded tree")
	}
	leftovers, _ := filepath.Glob(filepath.Join(l.Home, ".local/fennec/.node-seed-*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}

	// A later start, or a node the user or Fennec installed, is never replaced.
	if err := os.WriteFile(filepath.Join(node, "bin/node"), []byte("mine"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(node, "bin/node")); string(data) != "mine" {
		t.Fatal("an existing node installation was replaced")
	}
}

func TestInitWithoutSeedLeavesHomeAlone(t *testing.T) {
	l := testLayout(t)
	l.Seed = filepath.Join(t.TempDir(), "missing")
	if _, err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Home, ".local")); !os.IsNotExist(err) {
		t.Fatal("seeding created directories without a seed")
	}
}
