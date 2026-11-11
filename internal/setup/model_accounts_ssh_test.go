package setup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

func TestModelAccountsUsesOneSSHRequestAndPrivateStdin(t *testing.T) {
	home, remote := t.TempDir(), t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "providers.json"), []byte(`{"test":"fixture-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := &server.Server{Handler: func(s server.Session) {
		requests.Add(1)
		if s.RawCommand() != `"$HOME/.local/fennec/bin/fennec-terminal" sync-model-accounts` {
			t.Error("unexpected command or credential in argv")
			s.Exit(1)
			return
		}
		var request ModelAccountsRequest
		if json.NewDecoder(s).Decode(&request) != nil {
			t.Error("missing stdin envelope")
			s.Exit(1)
			return
		}
		result, err := reconcileModelAccounts(context.Background(), remote, request, func(context.Context, string, []ModelPlugin) error { return nil }, func(context.Context, string) error { return nil })
		if err != nil {
			t.Error(err)
			s.Exit(1)
			return
		}
		if err := json.NewEncoder(s).Encode(result); err != nil {
			t.Error(err)
		}
		s.Exit(0)
	}}
	srv.AddHostKey(signer)
	go srv.Serve(listener)
	defer srv.Close()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "fixture", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := SyncModelAccounts(context.Background(), client, home, nil)
	if err != nil || result.Migrated != 1 || requests.Load() != 1 {
		t.Fatalf("batch not confirmed: %+v calls=%d err=%v", result, requests.Load(), err)
	}
}
