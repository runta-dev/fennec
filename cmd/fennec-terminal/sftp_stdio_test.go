package main

import (
	"bytes"
	"context"
	"fennec/internal/commandstream"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestSFTPInstallationThroughResidentCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer output.Close()
		done <- terminalstream.ServeWithSockets(ctx, input, output, nil, nil, nil, nil, func(ctx context.Context, _ string) (net.Conn, error) { return commandstream.Dial(ctx), nil })
	}()
	transport := terminalstream.StartPipes(send, receive)
	defer func() { transport.Close(); cancel(); <-done }()
	conn, err := transport.DialSocket(ctx, terminalstream.SocketCommand, "")
	if err != nil {
		t.Fatal(err)
	}
	command := commandstream.NewStream(ctx, conn)
	defer command.Close()
	stdin, write := io.Pipe()
	read, stdout := io.Pipe()
	defer stdin.Close()
	defer write.Close()
	defer read.Close()
	defer stdout.Close()
	command.Stdin, command.Stdout = stdin, stdout
	exited := make(chan error, 1)
	go func() {
		defer stdout.Close()
		exited <- command.Run("'" + strings.ReplaceAll(binary, "'", "'\"'\"'") + "' sftp-stdio")
	}()
	client, err := sftp.NewClientPipe(read, write)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	payload := bytes.Repeat([]byte{0, 255, 7, 19}, 16384)
	file, err := client.Create("agent-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = client.Chmod("agent-fixture", 0700); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "agent-fixture"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("SFTP installation bytes changed", err)
	}
	info, err := os.Stat(filepath.Join(home, "agent-fixture"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("SFTP installation mode changed", err)
	}
	if _, err := transport.Ping(ctx); err != nil {
		t.Fatal("SFTP lost resident control", err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatal("SFTP EOF did not exit cleanly", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
