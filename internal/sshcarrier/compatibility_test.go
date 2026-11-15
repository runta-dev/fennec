package sshcarrier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/parser"
	"go/token"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestWorkerNamespacePreservesV5AndSeparatesEpochs(t *testing.T) {
	c := Config{Address: "example:22", User: "runtime", HostKey: []byte("host"), ClientKey: []byte("client"), KeyPath: "/private/key", Scope: "account"}
	data, _ := json.Marshal(c)
	legacy := sha256.Sum256(append([]byte("fennec-ssh-carrier-v5\x00"), data...))
	if c.key() != hex.EncodeToString(legacy[:12]) {
		t.Fatal("existing v5 worker no longer reusable; review and update the compatibility contract")
	}
	if c.keyForEpoch(workerCompatibilityEpoch+1) == c.key() {
		t.Fatal("incompatible workers share a socket/launchd identity")
	}
	next := c
	next.NoDelay = !c.NoDelay
	if next.key() == c.key() {
		t.Fatal("transport policy changed without namespace isolation")
	}
}

func TestIncompatibleLeaseCannotDisruptCompatiblePeer(t *testing.T) {
	f := newFixture(t, time.Minute)
	peer := f.lease(t)
	if err := runCommand(peer, "before"); err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(time.Second))
	if _, err := raw.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	conn, _, _, err := ssh.NewClientConn(raw, f.socket, &ssh.ClientConfig{
		User: "fennec-transport-v999", Auth: []ssh.AuthMethod{ssh.PublicKeys(f.signer)}, HostKeyCallback: ssh.FixedHostKey(f.signer.PublicKey()),
	})
	if err == nil {
		conn.Close()
		t.Fatal("incompatible lease authenticated")
	}
	if err := runCommand(peer, "after"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	replacement, err := dialLocal(ctx, f.socket, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := runCommand(replacement, "replacement"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 1 {
		t.Fatal("local version rejection replaced the upstream")
	}
}

func TestWorkerCannotImportApplicationServices(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if strings.HasPrefix(path, "fennec/") && path != "fennec/internal/connectivity" && path != "fennec/internal/launchprofile" {
				t.Errorf("transport-only worker acquired application dependency: %s imports %s", name, path)
			}
		}
	}
}
