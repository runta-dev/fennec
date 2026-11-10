package setup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// Exercise the real SSH execution adapter, including catalog parsing and
// readback, without executing user agents or contacting a model provider.
func TestReconcileRoutesOverSSH(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	applied := map[string]bool{}
	probes := 0
	srv := &server.Server{Handler: func(s server.Session) {
		command := s.RawCommand()
		if strings.HasPrefix(command, "curl ") {
			fmt.Fprint(s, `{"data":[{"id":"test/model"},{"id":"broken/model"}]}`)
			s.Exit(0)
			return
		}
		if command == "sh -s" {
			var content strings.Builder
			buffer := make([]byte, 4096)
			for {
				n, e := s.Read(buffer)
				content.Write(buffer[:n])
				if e != nil {
					break
				}
			}
			script := content.String()
			if strings.Contains(script, "<<'PROBE'") {
				mu.Lock()
				probes++
				mu.Unlock()
				if strings.Contains(script, "test/model") {
					s.Exit(0)
				} else {
					s.Exit(1)
				}
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range []string{"claude", "codex"} {
				if strings.Contains(script, "magpie '"+id+"'") {
					applied[id] = true
				}
			}
			if strings.Contains(script, "magpie 'codex'") {
				s.Exit(1)
			} else {
				s.Exit(0)
			}
			return
		}
		if strings.HasSuffix(command, "magpie 'claude' model") {
			fmt.Fprint(s, "Claude Code model test/model\n")
			s.Exit(0)
			return
		}
		s.Exit(127)
	}}
	srv.AddHostKey(signer)
	go srv.Serve(listener)
	defer srv.Close()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	results, err := ReconcileRoutes(context.Background(), client, []AgentRoute{
		{Agent: "claude", Model: "test/model"},
		{Agent: "codex", Model: "test/model"},
		{Agent: "pi", Model: "missing/model"},
		{Agent: "pi", Model: "broken/model"},
		{Agent: "opencode", Problem: "model_selection_required"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"configured", "configuration_failed", "verification_failed", "verification_failed", "model_selection_required"}
	for i, result := range results {
		if result.Status != want[i] {
			t.Fatalf("result %d: %+v", i, result)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 0 {
		t.Fatalf("configuration sync sent %d inference probes", probes)
	}
	if len(applied) != 2 {
		t.Fatalf("unexpected writes: %+v", applied)
	}
}
