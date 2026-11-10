package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverlapRejectsParentChildAndOtherAccount(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	sub := filepath.Join(root, "src")
	sibling := filepath.Join(base, "repo-other")
	os.MkdirAll(sub, 0700)
	os.MkdirAll(sibling, 0700)
	registry := filepath.Join(base, "registry")
	entry := Registration{Root: root, Session: "fennec-one", DataDirectory: filepath.Join(base, "data")}
	if err := Register(registry, entry); err != nil {
		t.Fatal(err)
	}
	if err := CheckOverlap(registry, entry); err != nil {
		t.Fatal("idempotent registration rejected", err)
	}
	for _, path := range []string{root, sub, base} {
		other := entry
		other.Root = path
		other.Session = "fennec-two"
		if CheckOverlap(registry, other) == nil {
			t.Fatal("overlap accepted", path)
		}
	}
	other := entry
	other.Root = sibling
	other.Session = "fennec-two"
	if err := CheckOverlap(registry, other); err != nil {
		t.Fatal("sibling rejected", err)
	}
}
func TestRegistryLockWaitIsCancellable(t *testing.T) {
	dir := t.TempDir()
	err := WithRegistryLock(context.Background(), dir, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if WithRegistryLock(ctx, dir, func() error { t.Fatal("concurrent lock entered"); return nil }) == nil {
			t.Fatal("cancel ignored")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
