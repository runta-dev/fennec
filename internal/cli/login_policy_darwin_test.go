package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test owns a unique, harmless launchd label and never starts the
// actual Fennec service, reads Keychain credentials, or touches cloud state.
func TestLaunchAgentWaitsForExplicitDemand(t *testing.T) {
	if os.Getenv("FENNEC_LAUNCHD_E2E") != "1" {
		t.Skip("explicit launchd policy probe required")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	binary := filepath.Join(dir, "probe")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n: > "+quote(marker)+"\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("com.runta.fennec.test-demand.%d.%d", os.Getpid(), time.Now().UnixNano())
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	plist := filepath.Join(dir, "probe.plist")
	data := bytes.ReplaceAll(daemonPlist(binary, filepath.Join(dir, "log")), []byte(daemonLabel), []byte(label))
	if err := os.WriteFile(plist, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer func() {
		_ = exec.Command("/bin/launchctl", "bootout", target).Run()
		deadline := time.Now().Add(2 * time.Second)
		for exec.Command("/bin/launchctl", "print", target).Run() == nil {
			if time.Now().After(deadline) {
				t.Error("test registration did not unload")
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("launchctl failed: %v %s", err, out)
		}
	}
	run("bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist)
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("daemon launched merely by loading its registration")
	}
	run("kickstart", target)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("explicit start did not launch")
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = os.Remove(marker)
	run("kill", "SIGKILL", target)
	deadline = time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an explicitly started service did not recover after a crash")
		}
		time.Sleep(25 * time.Millisecond)
	}
	run("bootout", target)
	deadline = time.Now().Add(2 * time.Second)
	for exec.CommandContext(ctx, "/bin/launchctl", "print", target).Run() == nil {
		if time.Now().After(deadline) {
			t.Fatal("registration did not unload")
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = os.Remove(marker)
	run("bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist)
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("fresh registration inherited demand from the previous login")
	}
}
