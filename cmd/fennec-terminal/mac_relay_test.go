package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fennec/internal/localssh"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestMacRelayCommandChild(t *testing.T) {
	if os.Getenv("FENNEC_RELAY_COMMAND_CHILD") != "1" {
		return
	}
	os.Args = []string{"fennec-terminal", "mac-relay", "aabbccdd", "1"}
	code, err := run()
	if err != nil {
		os.Exit(99)
	}
	os.Exit(code)
}

// Exercise the real signal handler and OS pipes: closing only stdout can
// retire the master while leaving the relay stuck reading stdin forever.
func TestMacRelayCommandExitsOnSIGTERM(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh", "fennec")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := localssh.EnsureRelayHostKey(dir); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aabbccdd"), pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aabbccdd.known_hosts"), append([]byte("fennec-aabbccdd "), ssh.MarshalAuthorizedKey(identity.PublicKey())...), 0600); err != nil {
		t.Fatal(err)
	}
	trusted, err := os.ReadFile(filepath.Join(dir, "relay_known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, hostKey, _, _, err := ssh.ParseKnownHosts(trusted)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestMacRelayCommandChild$")
	command.Env = append(os.Environ(), "HOME="+home, "FENNEC_RELAY_COMMAND_CHILD=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() { command.Process.Kill() })
	connection, channels, requests, err := ssh.NewClientConn(relayProcessPipe{Reader: stdout, WriteCloser: stdin}, "relay", &ssh.ClientConfig{
		User: "fennec", Auth: []ssh.AuthMethod{ssh.PublicKeys(identity)}, HostKeyCallback: ssh.FixedHostKey(hostKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(connection, channels, requests)
	defer client.Close()
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("relay exit: %v: %s", err, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM left the relay alive with an open stdin")
	}
}

type relayProcessPipe struct {
	io.Reader
	io.WriteCloser
}

func (relayProcessPipe) LocalAddr() net.Addr              { return &net.UnixAddr{Name: "pipe", Net: "unix"} }
func (relayProcessPipe) RemoteAddr() net.Addr             { return &net.UnixAddr{Name: "pipe", Net: "unix"} }
func (relayProcessPipe) SetDeadline(time.Time) error      { return nil }
func (relayProcessPipe) SetReadDeadline(time.Time) error  { return nil }
func (relayProcessPipe) SetWriteDeadline(time.Time) error { return nil }
