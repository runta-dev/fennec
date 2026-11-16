package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fennec/internal/commandstream"
	"fennec/internal/project"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestProjectUploadThroughResidentCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	if err = os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(binary, entry); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	payload := bytes.Repeat([]byte{0, 17, 255, 6}, 32768)
	if err = os.WriteFile(filepath.Join(source, "file"), payload, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	manifest := project.Manifest{Root: source, WorkingDirectory: "sub", Bytes: int64(len(payload)), Files: []project.File{{Path: "file", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]), Executable: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer output.Close()
		serverDone <- terminalstream.ServeWithSockets(ctx, input, output, nil, nil, nil, nil, func(ctx context.Context, _ string) (net.Conn, error) { return commandstream.Dial(ctx), nil })
	}()
	transport := terminalstream.StartPipes(send, receive)
	defer func() { transport.Close(); cancel(); <-serverDone }()
	client := new(ssh.Client)
	unregister := commandstream.Register(client, func(ctx context.Context) (net.Conn, error) {
		return transport.DialSocket(ctx, terminalstream.SocketCommand, "")
	})
	defer unregister()
	result, err := project.Upload(ctx, client, strings.Repeat("a", 32), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || result.Bytes != int64(len(payload)) || filepath.Base(result.Directory) != "sub" {
		t.Fatal(result)
	}
	target := filepath.Join(filepath.Dir(result.Directory), "file")
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("snapshot bytes changed", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("executable mode changed", err)
	}
	if err = os.WriteFile(target, []byte("cloud edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = project.Upload(ctx, client, strings.Repeat("a", 32), manifest); err == nil {
		t.Fatal("existing snapshot overwritten")
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "cloud edit" {
		t.Fatal("cloud edit lost", err)
	}
}
