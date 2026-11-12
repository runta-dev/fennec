package sandbox

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGuardPathEnforcesProtectedAndWorkspaceBoundaries(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(realHome, ".fennec-guard-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	workspace := filepath.Join(home, "repos/project")
	outside := filepath.Join(home, "Documents")
	if err := os.MkdirAll(filepath.Join(workspace, ".git/hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}

	wsPolicy := Policy{Mode: ModeWorkspace, WritableRoots: []string{workspace}}
	if err := GuardPath(wsPolicy, home, filepath.Join(home, ".ssh/id_ed25519"), false); err == nil {
		t.Fatal("expected read of ~/.ssh to be blocked")
	}
	if err := GuardPath(wsPolicy, home, filepath.Join(home, ".config/fennec/ipc/control.sock"), false); err == nil {
		t.Fatal("expected read of ~/.config/fennec to be blocked")
	}
	if err := GuardPath(wsPolicy, home, filepath.Join(workspace, "main.go"), true); err != nil {
		t.Fatalf("expected write inside workspace to succeed: %v", err)
	}
	if err := GuardPath(wsPolicy, home, filepath.Join(workspace, ".git/hooks/pre-commit"), true); err == nil {
		t.Fatal("expected write to .git/hooks to be blocked")
	}
	if err := GuardPath(wsPolicy, home, filepath.Join(outside, "notes.txt"), true); err == nil {
		t.Fatal("expected write outside workspace to be blocked")
	}

	roPolicy := Policy{Mode: ModeReadOnly, WritableRoots: []string{workspace}}
	if err := GuardPath(roPolicy, home, filepath.Join(workspace, "main.go"), true); err == nil {
		t.Fatal("expected read-only mode to block workspace writes")
	}
}

func TestMacOSSeatbeltKernelEnforcement(t *testing.T) {
	if runtime.GOOS != "darwin" || !Available() {
		t.Skip("macOS sandbox-exec kernel test")
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	fakeHome, err := os.MkdirTemp(realHome, ".fennec-seatbelt-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(fakeHome)
	workspace := filepath.Join(fakeHome, "workspace")
	outside := filepath.Join(fakeHome, "outside")
	sshDir := filepath.Join(fakeHome, ".ssh")
	fennecDir := filepath.Join(fakeHome, ".config/fennec")
	for _, d := range []string{filepath.Join(workspace, ".git/hooks"), outside, sshDir, fennecDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	secretKey := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(secretKey, []byte("PRIVATE_SSH_KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	fennecState := filepath.Join(fennecDir, "settings.json")
	if err := os.WriteFile(fennecState, []byte(`{"sandbox_mode":"workspace"}`), 0600); err != nil {
		t.Fatal(err)
	}

	run := func(p Policy, shell string) (int, string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd, _ := Command(ctx, p, fakeHome, workspace, shell)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			} else {
				code = 1
			}
		}
		return code, stdout.String(), stderr.String()
	}

	wsPolicy := Policy{Mode: ModeWorkspace, Network: false, WritableRoots: []string{workspace}}

	// 1. Allowed write inside workspace succeeds.
	code, _, stderr := run(wsPolicy, "printf 'ok' > allowed.txt && cat allowed.txt")
	if code != 0 {
		t.Fatalf("workspace write failed unexpectedly: code=%d stderr=%s", code, stderr)
	}

	// 2. Write outside workspace is blocked by kernel Seatbelt.
	code, _, stderr = run(wsPolicy, "printf 'escape' > '"+filepath.Join(outside, "escape.txt")+"'")
	if code == 0 || !DetectSandboxViolation(ModeWorkspace, code, stderr) {
		t.Fatalf("expected kernel Seatbelt to block write outside workspace: code=%d stderr=%s", code, stderr)
	}

	// 3. Write to .git/hooks inside workspace is blocked by kernel Seatbelt.
	code, _, stderr = run(wsPolicy, "printf '#!/bin/sh' > .git/hooks/post-checkout")
	if code == 0 {
		t.Fatal("expected kernel Seatbelt to block .git/hooks tampering")
	}

	// 4. Reading ~/.ssh or ~/.config/fennec is blocked by kernel Seatbelt.
	code, out, _ := run(wsPolicy, "cat '"+secretKey+"'")
	if code == 0 || strings.Contains(out, "PRIVATE_SSH_KEY") {
		t.Fatal("expected kernel Seatbelt to block reading ~/.ssh")
	}
	code, _, _ = run(wsPolicy, "cat '"+fennecState+"'")
	if code == 0 {
		t.Fatal("expected kernel Seatbelt to block reading ~/.config/fennec")
	}

	// 5. Read-only mode blocks writes even inside workspace.
	roPolicy := Policy{Mode: ModeReadOnly, Network: false, WritableRoots: []string{workspace}}
	code, _, _ = run(roPolicy, "printf 'nope' > readonly-test.txt")
	if code == 0 {
		t.Fatal("expected read-only Seatbelt profile to block workspace writes")
	}

	// 6. Network disabled blocks TCP connections; Network enabled permits them.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = conn.Write([]byte("PONG\n"))
			conn.Close()
		}
	}()
	port := strings.Split(ln.Addr().String(), ":")[1]
	code, _, _ = run(wsPolicy, "/usr/bin/nc -w 2 127.0.0.1 "+port)
	if code == 0 {
		t.Fatal("expected Seatbelt network=false to block outbound TCP")
	}

	netPolicy := Policy{Mode: ModeWorkspace, Network: true, WritableRoots: []string{workspace}}
	code, out, stderr = run(netPolicy, "/usr/bin/nc -w 2 127.0.0.1 "+port)
	if code != 0 || !strings.Contains(out, "PONG") {
		t.Fatalf("expected Seatbelt network=true to permit TCP: code=%d out=%q stderr=%q", code, out, stderr)
	}
}
