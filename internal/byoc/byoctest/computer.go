// Package byoctest provides an in-process stand-in for the Fennec cloud
// image's SSH service, for tests of pairing and self-hosted connections.
package byoctest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fennec/internal/byoc"
	"net"
	"strconv"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Machine is the default machine ID the fake computer reports.
const Machine = "0b9f6c1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"

// Computer accepts one authorized device key for byoc.DefaultUser, answers
// the identity read, and records every other command it is asked to run.
type Computer struct {
	HostSigner ssh.Signer
	listener   net.Listener
	authorized atomic.Value // ssh.PublicKey
	identity   atomic.Value // string
	commands   atomic.Int64
}

// New starts a fake computer that stops when the test ends.
func New(t testing.TB) *Computer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &Computer{HostSigner: hostSigner, listener: listener}
	c.SetIdentity(`{"machine_id":"` + Machine + `"}`)
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		allowed, _ := c.authorized.Load().(ssh.PublicKey)
		if meta.User() != byoc.DefaultUser || allowed == nil || string(allowed.Marshal()) != string(key.Marshal()) {
			return nil, errors.New("unauthorized")
		}
		return nil, nil
	}}
	config.AddHostKey(hostSigner)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go c.serve(raw, config)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return c
}

// SetIdentity replaces the raw identity document the computer reports.
func (c *Computer) SetIdentity(raw string) { c.identity.Store(raw) }

// Commands counts commands other than the identity read.
func (c *Computer) Commands() int64 { return c.commands.Load() }

// Fingerprint is the SHA256 fingerprint of the computer's host key.
func (c *Computer) Fingerprint() string { return ssh.FingerprintSHA256(c.HostSigner.PublicKey()) }

// Endpoint is where the computer listens.
func (c *Computer) Endpoint() byoc.Endpoint {
	host, port, _ := net.SplitHostPort(c.listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return byoc.Endpoint{Host: host, Port: p, User: byoc.DefaultUser}
}

// Authorize accepts home's device key, as the image does for the key the
// user supplies at deployment.
func (c *Computer) Authorize(t testing.TB, home string) {
	t.Helper()
	signer, err := byoc.DeviceKey(home)
	if err != nil {
		t.Fatal(err)
	}
	c.authorized.Store(signer.PublicKey())
}

// Pair authorizes home's device key, pairs with the computer and saves the
// pairing record.
func (c *Computer) Pair(t testing.TB, home string) byoc.Pairing {
	t.Helper()
	c.Authorize(t, home)
	pairing, client, err := byoc.Discover(context.Background(), home, c.Endpoint(), c.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	if err := byoc.Save(home, pairing); err != nil {
		t.Fatal(err)
	}
	return pairing
}

func (c *Computer) serve(raw net.Conn, config *ssh.ServerConfig) {
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		raw.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for opened := range channels {
		channel, reqs, err := opened.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range reqs {
				if req.Type != "exec" || len(req.Payload) < 4 {
					req.Reply(false, nil)
					continue
				}
				length := binary.BigEndian.Uint32(req.Payload[:4])
				if int(length) > len(req.Payload)-4 {
					req.Reply(false, nil)
					return
				}
				command := string(req.Payload[4 : 4+length])
				req.Reply(true, nil)
				status := uint32(0)
				if command == `cat -- "$HOME/`+byoc.IdentityPath+`"` {
					channel.Write([]byte(c.identity.Load().(string)))
				} else {
					c.commands.Add(1)
					status = 127
				}
				channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, status))
				return
			}
		}()
	}
}
