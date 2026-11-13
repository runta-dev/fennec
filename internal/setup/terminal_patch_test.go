package setup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	gliderssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// The cloud side of apply-patch, run by the fake "old binary" below.
func TestHelperApplyPatch(t *testing.T) {
	old := os.Getenv("FENNEC_TEST_APPLY_PATCH")
	if old == "" {
		t.Skip("helper")
	}
	if err := ApplyTerminalPatch(old, os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type fakeCloud struct {
	home   string
	client *ssh.Client
	mu     sync.Mutex
	stdin  []int // bytes received per command
}

func startFakeCloud(t *testing.T) *fakeCloud {
	t.Helper()
	cloud := &fakeCloud{home: t.TempDir()}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	server := &gliderssh.Server{
		PasswordHandler: func(gliderssh.Context, string) bool { return true },
		Handler: func(s gliderssh.Session) {
			input, _ := io.ReadAll(s)
			cloud.mu.Lock()
			cloud.stdin = append(cloud.stdin, len(input))
			cloud.mu.Unlock()
			cmd := exec.Command("sh", "-c", s.RawCommand())
			cmd.Env = append(os.Environ(), "HOME="+cloud.home)
			cmd.Stdin = bytes.NewReader(input)
			cmd.Stdout = s
			cmd.Stderr = s.Stderr()
			code := 0
			if err := cmd.Run(); err != nil {
				code = 1
			}
			s.Exit(code)
		},
	}
	server.AddHostKey(signer)
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "t", Auth: []ssh.AuthMethod{ssh.Password("x")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	cloud.client = client
	return cloud
}

// installOld puts a binary on the fake cloud as the active terminal service.
func (c *fakeCloud) installOld(t *testing.T, data []byte) string {
	sum := sha256.Sum256(data)
	version := hex.EncodeToString(sum[:])
	dir := filepath.Join(c.home, ".local/fennec/terminal", version)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "fennec-terminal"), data, 0o700)
	os.MkdirAll(filepath.Join(c.home, ".local/fennec/bin"), 0o700)
	os.Symlink(filepath.Join(dir, "fennec-terminal"), filepath.Join(c.home, ".local/fennec/bin/fennec-terminal"))
	return version
}

func (c *fakeCloud) uploaded() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	max := 0
	for _, n := range c.stdin {
		if n > max {
			max = n
		}
	}
	return max
}

func terminalBuilds(t *testing.T, oldScript string) (old, new []byte) {
	shared := make([]byte, 2<<20)
	rand.Read(shared)
	old = append([]byte(oldScript+"\n#"), shared...)
	new = append([]byte("\x7fELF"), shared...)
	new = append(new, "changed tail"...)
	return old, new
}

func useTerminalCache(t *testing.T) string {
	dir := t.TempDir()
	previous := terminalCacheDir
	terminalCacheDir = func() string { return dir }
	t.Cleanup(func() { terminalCacheDir = previous })
	return dir
}

func installedBytes(t *testing.T, c *fakeCloud) []byte {
	data, err := os.ReadFile(filepath.Join(c.home, ".local/fennec/bin/fennec-terminal"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// An upgrade sends a small patch against the binary already on the cloud.
func TestTerminalUpgradeSendsPatch(t *testing.T) {
	cache := useTerminalCache(t)
	self, _ := os.Executable()
	old, new := terminalBuilds(t, "#!/bin/sh\nFENNEC_TEST_APPLY_PATCH=\"$2\" exec '"+self+"' -test.run=TestHelperApplyPatch")
	cloud := startFakeCloud(t)
	cloud.installOld(t, old)
	os.WriteFile(filepath.Join(cache, "fennec-terminal-linux-"+archOf(t, cloud)), old, 0o600)
	artifacts := t.TempDir()
	os.WriteFile(filepath.Join(artifacts, "fennec-terminal-linux-amd64"), new, 0o700)
	os.WriteFile(filepath.Join(artifacts, "fennec-terminal-linux-arm64"), new, 0o700)

	if err := InstallTerminalBinary(context.Background(), cloud.client, artifacts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installedBytes(t, cloud), new) {
		t.Fatal("installed binary differs")
	}
	if sent := cloud.uploaded(); sent > len(new)/20 {
		t.Fatalf("sent %d bytes for a %d byte binary; expected a patch", sent, len(new))
	}
	if cached, _ := os.ReadFile(filepath.Join(cache, "fennec-terminal-linux-"+archOf(t, cloud))); !bytes.Equal(cached, new) {
		t.Fatal("cache does not hold the installed binary")
	}
}

// A cloud binary that predates apply-patch still upgrades with a full upload.
func TestTerminalUpgradeFallsBackToFullUpload(t *testing.T) {
	cache := useTerminalCache(t)
	old, new := terminalBuilds(t, "#!/bin/sh\nexit 1")
	cloud := startFakeCloud(t)
	cloud.installOld(t, old)
	os.WriteFile(filepath.Join(cache, "fennec-terminal-linux-"+archOf(t, cloud)), old, 0o600)
	artifacts := t.TempDir()
	os.WriteFile(filepath.Join(artifacts, "fennec-terminal-linux-amd64"), new, 0o700)
	os.WriteFile(filepath.Join(artifacts, "fennec-terminal-linux-arm64"), new, 0o700)

	if err := InstallTerminalBinary(context.Background(), cloud.client, artifacts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installedBytes(t, cloud), new) {
		t.Fatal("installed binary differs")
	}
	if sent := cloud.uploaded(); sent < len(new)/20 {
		t.Fatalf("expected a full upload, sent %d", sent)
	}
}

func archOf(t *testing.T, c *fakeCloud) string {
	out, _ := exec.Command("uname", "-m").Output()
	return map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[string(bytes.TrimSpace(out))]
}
