package localssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentPairingPreservesEveryKey(t *testing.T) {
	dir := t.TempDir()
	keys := []ssh.PublicKey{}
	for i := 0; i < 8; i++ {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, _ := ssh.NewPublicKey(public)
		keys = append(keys, key)
	}
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(key ssh.PublicKey) {
			defer wg.Done()
			if err := addPairingKey(context.Background(), dir, key); err != nil {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	path := filepath.Join(dir, "authorized_keys")
	for _, key := range keys {
		if !authorizedKey(path, key) {
			t.Fatal("concurrent pairing lost key")
		}
	}
	before, _ := os.ReadFile(path)
	if err := addPairingKey(context.Background(), dir, keys[0]); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("duplicate pairing changed keys")
	}
}

func TestLatePairingCannotRestorePreviousOwner(t *testing.T) {
	dir := t.TempDir()
	if err := bindLocalOwner(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(public)
	if err := bindLocalOwner(dir, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := addPairingKey(context.Background(), dir, key, "alice"); err == nil {
		t.Fatal("stale owner paired")
	}
	if authorizedKey(filepath.Join(dir, "authorized_keys"), key) {
		t.Fatal("old key restored")
	}
	if err := addPairingKey(context.Background(), dir, key, "bob"); err != nil {
		t.Fatal(err)
	}
}
