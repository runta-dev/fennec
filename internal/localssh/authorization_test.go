package localssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairingRevocationAndPermissions(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, ssh.MarshalAuthorizedKey(key), 0600); err != nil {
		t.Fatal(err)
	}
	if !authorizedKey(path, key) {
		t.Fatal("valid pairing rejected")
	}
	os.Chmod(path, 0666)
	if authorizedKey(path, key) {
		t.Fatal("writable pairing accepted")
	}
	os.Chmod(path, 0600)
	os.WriteFile(path, nil, 0600)
	if authorizedKey(path, key) {
		t.Fatal("revoked pairing accepted")
	}
}
func TestApprovalCancelledWithoutUI(t *testing.T) {
	b := &Broker{pending: map[string]chan Decision{}, output: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan bool, 1)
	go func() { done <- b.approve(ctx, "true", "test") }()
	select {
	case allowed := <-done:
		if allowed {
			t.Fatal("cancelled approval accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release request")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) != 0 {
		t.Fatal("pending request leaked")
	}
}

func TestNeverAskPersistsPerDevice(t *testing.T) {
	directory := t.TempDir()
	b := &Broker{pending: map[string]chan Decision{}, output: io.Discard, directory: directory}
	b.remember("SHA256:device-one")
	restored := &Broker{pending: map[string]chan Decision{}, output: io.Discard, directory: directory}
	if !restored.trusted("SHA256:device-one") || restored.trusted("SHA256:device-two") {
		t.Fatal("device trust scope incorrect")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !restored.approve(ctx, "true", "SHA256:device-one") {
		t.Fatal("remembered device asked again")
	}
}

func TestClearTrustRequiresApprovalAgain(t *testing.T) {
	b := &Broker{pending: map[string]chan Decision{}, output: io.Discard, directory: t.TempDir()}
	b.remember("device")
	if err := b.clearTrust(); err != nil {
		t.Fatal(err)
	}
	if b.trusted("device") {
		t.Fatal("trust survived reset")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b.approve(ctx, "test", "device") {
		t.Fatal("reset device bypassed approval")
	}
	if err := b.clearTrust(); err != nil {
		t.Fatal("reset not idempotent", err)
	}
}
