package setup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

func TestPublicCheckPreservesScriptAndRemoteFailure(t *testing.T) {
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
	commands := make(chan string, 3)
	srv := &server.Server{Handler: func(s server.Session) {
		commands <- s.RawCommand()
		command := exec.CommandContext(s.Context(), "/bin/sh", "-c", s.RawCommand())
		command.Stdin, command.Stdout, command.Stderr = s, s, s.Stderr()
		code := 0
		if err := command.Run(); err != nil {
			code = command.ProcessState.ExitCode()
		}
		s.Exit(code)
	}}
	srv.AddHostKey(signer)
	go srv.Serve(listener)
	defer srv.Close()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	script := "value=\"a'b\"\ntest \"$value\" = \"a'b\"\n"
	if err := ExecPublicCheck(ctx, client, script); err != nil {
		t.Fatal(err)
	}
	if command := <-commands; !strings.HasPrefix(command, "sh -c ") {
		t.Fatalf("check still needs stdin: %s", command)
	}
	err = ExecPublicCheck(ctx, client, "printf 'checkpoint missing' >&2\nexit 17")
	var status *ssh.ExitError
	if !errors.As(err, &status) || status.ExitStatus() != 17 || !strings.Contains(err.Error(), "checkpoint missing") {
		t.Fatalf("lost failed check: %v", err)
	}
	<-commands
	if err := Exec(ctx, client, "test 'credential-marker' = 'credential-marker'"); err != nil {
		t.Fatal(err)
	}
	if command := <-commands; command != "sh -s" {
		t.Fatalf("private script exposed in exec request: %s", command)
	}
}
