package setup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// Exercise one SSH request and the local reconciliation/readback logic without
// executing user agents or contacting a model provider.
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
	requests := 0
	srv := &server.Server{Handler: func(s server.Session) {
		command := s.RawCommand()
		prefix := `"$HOME/.local/fennec/bin/fennec-terminal" reconcile-routes `
		if !strings.HasPrefix(command, prefix) {
			s.Exit(127)
			return
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, prefix))
		var routes []AgentRoute
		if err != nil || json.Unmarshal(data, &routes) != nil {
			s.Exit(1)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		results, err := reconcileRoutesWith(s.Context(), routes, func(_ context.Context, script string) error {
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(script, "<<'PROBE'") || strings.Contains(script, "curl ") {
				probes++
				return fmt.Errorf("unexpected probe")
			}
			for _, id := range []string{"claude", "codex", "pi"} {
				if strings.Contains(script, "magpie '"+id+"'") {
					applied[id] = true
				}
			}
			if strings.Contains(script, "magpie 'codex'") {
				return fmt.Errorf("write failed")
			}
			return nil
		}, func(_ context.Context, agent string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			if agent == "claude" {
				if !applied[agent] {
					return []byte("Claude Code model old/model\n"), nil
				}
				return []byte("Claude Code model test/model\n"), nil
			}
			if agent == "codex" {
				return []byte("Codex model old/model\n"), nil
			}
			if agent == "pi" {
				return []byte("Pi model different/model\n"), nil
			}
			t.Error("readback followed a failed or skipped write")
			return nil, fmt.Errorf("unexpected readback")
		})
		if err != nil {
			t.Error(err)
			s.Exit(1)
			return
		}
		if err := json.NewEncoder(s).Encode(results); err != nil {
			t.Error(err)
		}
		s.Exit(0)
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
	if len(applied) != 3 || requests != 1 {
		t.Fatalf("unexpected writes: %+v", applied)
	}
}

func TestMatchingRouteReadbackSkipsConfigurationWrite(t *testing.T) {
	reads := 0
	results, err := reconcileRoutesWith(context.Background(), []AgentRoute{{Agent: "codex", Model: "fixture/model"}}, func(context.Context, string) error { t.Fatal("unchanged route was rewritten"); return nil }, func(context.Context, string) ([]byte, error) {
		reads++
		return []byte("Codex model fixture/model\n"), nil
	})
	if err != nil || len(results) != 1 || results[0].Status != "configured" || reads != 1 {
		t.Fatalf("%+v reads=%d err=%v", results, reads, err)
	}
}

func TestUnconfiguredRouteDoesNotOpenSSHChannel(t *testing.T) {
	results, err := ReconcileRoutes(context.Background(), nil, []AgentRoute{{Agent: "opencode", Problem: "model_selection_required"}})
	if err != nil || len(results) != 1 || results[0].Status != "model_selection_required" {
		t.Fatalf("%+v %v", results, err)
	}
}
