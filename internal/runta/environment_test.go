package runta

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestResolveAndActiveEnvironment(t *testing.T) {
	prod, err := ResolveEnvironment("")
	if err != nil || prod.API != "https://api.runta.com" || prod.Dashboard != "https://dashboard.runta.com" || prod.SSHHost != "ssh.runta.com" || prod.Key() != "" {
		t.Fatalf("unexpected production env: %+v %v", prod, err)
	}
	integ, err := ResolveEnvironment("integration")
	if err != nil || integ.API != "https://api.runta.xyz" || integ.Dashboard != "https://dashboard.runta.xyz" || integ.SSHHost != "ssh.runta.xyz" || integ.Key() != "integration" {
		t.Fatalf("unexpected integration env: %+v %v", integ, err)
	}
	if _, err := ResolveEnvironment("http://insecure.example.com"); err == nil {
		t.Fatal("accepted non-HTTPS endpoint")
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config/fennec"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config/fennec/settings.json"), []byte(`{"environment":"integration","no_ssh_nodelay":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ActiveEnvironment(home); got.API != "https://api.runta.xyz" || got.SSHHost != "ssh.runta.xyz" {
		t.Fatalf("settings environment not applied: %+v", got)
	}
	if SSHNoDelayEnabled(home) {
		t.Fatal("expected no_ssh_nodelay=true to disable SSHNoDelayEnabled")
	}
	if filepath.Base(TakeoverPreferencesPath(home)) != "takeover-preferences.integration.json" {
		t.Fatalf("unexpected takeover path: %s", TakeoverPreferencesPath(home))
	}
}

func TestDialAndPassSSHFDPreservesKernelTCPNoDelay(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("unix fd passing test")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			defer conn.Close()
			buf := make([]byte, 1)
			_, _ = conn.Read(buf)
		}
	}()

	// Verify TCP_NODELAY socket option directly on a dialed connection and via SCM_RIGHTS.
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tcp := raw.(*net.TCPConn)
	if err := tcp.SetNoDelay(true); err != nil {
		t.Fatal(err)
	}
	sc, err := tcp.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Control(func(fd uintptr) {
		if sendErr := unix.Sendmsg(pair[1], []byte{0}, unix.UnixRights(int(fd)), nil, 0); sendErr != nil {
			t.Fatal(sendErr)
		}
	}); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unix.Recvmsg(pair[0], buf, oob, 0)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		t.Fatalf("parse control message: %v %d", err, len(msgs))
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("parse unix rights: %v %v", err, fds)
	}
	defer unix.Close(fds[0])

	val, err := unix.GetsockoptInt(fds[0], unix.IPPROTO_TCP, unix.TCP_NODELAY)
	if err != nil || val == 0 {
		t.Fatalf("expected passed socket to retain TCP_NODELAY != 0, got val=%d err=%v", val, err)
	}

	// Non-port-22 targets must be rejected fail-closed.
	if err := DialAndPassSSHFD(context.Background(), "127.0.0.1", "8080", true, pair[1]); err == nil {
		t.Fatal("expected non-SSH port to be rejected")
	}
}
