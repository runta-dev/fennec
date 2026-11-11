package terminalstream

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHTransportClosePreservesSharedConnection(t *testing.T) {
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
	defer listener.Close()
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		conn, channels, requests, err := ssh.NewServerConn(raw, config)
		if err != nil {
			raw.Close()
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					if request.Type != "exec" {
						request.Reply(false, nil)
						continue
					}
					var command struct{ Command string }
					if ssh.Unmarshal(request.Payload, &command) != nil {
						request.Reply(false, nil)
						return
					}
					request.Reply(true, nil)
					if command.Command == "terminal" {
						_ = Serve(context.Background(), channel, channel, nil)
					} else {
						_, _ = channel.Write([]byte("still connected"))
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
					return
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	transport, err := StartSSH(client, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if transport.Supports("reconcile_routes") {
		t.Fatal("non-operation server advertised route support")
	}
	transport.Close()
	peer, err := client.NewSession()
	if err != nil {
		t.Fatalf("shared SSH was closed: %v", err)
	}
	defer peer.Close()
	output, err := peer.Output("check")
	if err != nil || string(output) != "still connected" {
		t.Fatalf("peer channel failed: %q %v", output, err)
	}
}
