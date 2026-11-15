package localssh

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	gliderssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

func TestExistingRelayRequiresRevisionProcessIdentityAndListener(t *testing.T) {
	home := t.TempDir()
	device := "aabbccdd"
	base := filepath.Join(home, ".local/fennec/relay-"+device)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	state := map[string]any{"pid": 123, "bridge_port": port, "agent_port": port, "revision": fmt.Sprintf("%x", sha256.Sum256([]byte(relaySource)))}
	save := func() {
		raw, _ := json.Marshal(state)
		if err := os.WriteFile(filepath.Join(base, "state.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	read := func(string) ([]byte, error) {
		return []byte("python3\x00" + filepath.Join(base, "relay.py") + "\x00--state\x00" + filepath.Join(base, "state.json") + "\x00"), nil
	}
	result, err := inspectExistingRelay(context.Background(), home, device, read)
	if err != nil || !result.Ready || result.NeedsSetup {
		t.Fatalf("valid relay: %+v %v", result, err)
	}
	if _, err = inspectExistingRelay(context.Background(), home, device, func(string) ([]byte, error) { return []byte("unrelated process"), nil }); err == nil {
		t.Fatal("accepted unrelated process at recorded PID")
	}
	result, err = inspectExistingRelay(context.Background(), home, device, func(string) ([]byte, error) { return nil, os.ErrNotExist })
	if err != nil || !result.NeedsSetup {
		t.Fatal("dead relay did not request cold setup", result, err)
	}
	state["revision"] = "old"
	save()
	result, err = inspectExistingRelay(context.Background(), home, device, read)
	if err != nil || !result.NeedsSetup {
		t.Fatal("stale relay revision reused", result, err)
	}
	state["revision"] = fmt.Sprintf("%x", sha256.Sum256([]byte(relaySource)))
	save()
	listener.Close()
	result, err = inspectExistingRelay(context.Background(), home, device, read)
	if err != nil || !result.NeedsSetup {
		t.Fatal("closed relay port reused", result, err)
	}
}

func TestResidentConnectorFailureNeverReplaysOrClosesPeers(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		var commands atomic.Int32
		client := linkLeaseClient(t, func(server *gliderssh.Server) {
			server.Handler = func(s gliderssh.Session) { commands.Add(1); s.Exit(0) }
		})
		expected := errors.New("reply lost after mutation")
		config := LinkConfig{RuntimeID: "owned", Directory: t.TempDir(), VerifiedOwner: "owner", SharedSSH: func(context.Context) (*ssh.Client, error) { return client, nil }, Bootstrap: func(context.Context, LinkBootstrap) (LinkBootstrapResult, error) {
			if ambiguous {
				return LinkBootstrapResult{}, nil
			}
			return LinkBootstrapResult{}, expected
		}}
		err := linkOnce(context.Background(), nil, config, "aabbccdd", "fixture.local", "unused", "owner", func(string, string) {})
		if err == nil || (!ambiguous && !errors.Is(err, expected)) || commands.Load() != 0 {
			t.Fatal("uncertain bootstrap retried over exec", err, commands.Load())
		}
		session, err := client.NewSession()
		if err != nil {
			t.Fatal("peer connection closed", err)
		}
		session.Close()
	}
}
