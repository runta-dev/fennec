package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fennec/internal/setup"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

func TestSSHConfigurationDoesNotProbeInference(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	srv := &server.Server{Handler: func(session server.Session) {
		command := session.RawCommand()
		switch {
		case strings.HasPrefix(command, "curl "):
			fmt.Fprint(session, `{"data":[{"id":"fixture/model"}]}`)
		case command == "sh -s":
			raw, _ := io.ReadAll(session)
			if strings.Contains(string(raw), "<<'PROBE'") && probes.Add(1) == 1 {
				session.Exit(75)
				return
			}
		case strings.HasSuffix(command, "magpie 'codex' model"):
			fmt.Fprint(session, "Codex model fixture/model\n")
		default:
			session.Exit(127)
			return
		}
		session.Exit(0)
	}}
	srv.AddHostKey(signer)
	go srv.Serve(listener)
	defer srv.Close()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	route := setup.AgentRoute{Agent: "codex", Model: "fixture/model"}
	configure := func(ctx context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
		return setup.ReconcileRoutes(ctx, client, []setup.AgentRoute{route})
	}
	initial, err := configure(ctx, route)
	if err != nil || len(initial) != 1 || initial[0].Status != "configured" || initial[0].Model != route.Model {
		t.Fatal(initial, err)
	}
	if probes.Load() != 0 {
		t.Fatal("startup sent inference probe")
	}
}
