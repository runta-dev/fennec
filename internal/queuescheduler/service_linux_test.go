//go:build linux

package queuescheduler

import (
	"context"
	"errors"
	"fennec/internal/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServiceEnvironmentUsesExistingCloudLaunchLookup(t *testing.T) {
	home := t.TempDir()
	unit := string(queueServiceUnit(home, "/bin/fennec-terminal"))
	var path string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "Environment=\"") {
			value, err := strconv.Unquote(strings.TrimPrefix(line, "Environment="))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(value, "PATH=") {
				path = strings.TrimPrefix(value, "PATH=")
			}
		}
	}
	if path == "" {
		t.Fatal("service has no agent PATH")
	}
	t.Setenv("PATH", path)
	for _, name := range []string{"claude", "codex", "pi"} {
		binary := filepath.Join(home, ".local/fennec/agents/bin", name)
		if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		got, err := runtime.NewLocalRuntime(home).Lookup(name)
		if err != nil || got != binary {
			t.Fatal(name, got, err)
		}
	}
}

func TestServiceWithoutSystemdRunsOneDetachedScheduler(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not installed")
	}
	old := systemdRoot
	systemdRoot = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { systemdRoot = old })
	if Systemd() {
		t.Fatal("systemd detected without its marker directory")
	}
	home := t.TempDir()
	binary := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	// Stands in for "queue-service run": counts starts, then holds the run
	// lock like the real scheduler until the test kills it.
	script := "#!/bin/sh\n[ \"$1 $2\" = \"queue-service run\" ] || exit 2\necho start >> \"$HOME/starts\"\necho $$ > \"$HOME/pid\"\nexec 9>\"$HOME/.local/fennec/queue-service/run.lock\"\nflock -n 9 || exit 0\nexec sleep 60\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	waitLocked := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			release, err := LockService(home)
			if errors.Is(err, ErrServiceRunning) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			release()
			if time.Now().After(deadline) {
				t.Fatal("scheduler did not take the run lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	starts := func() int {
		data, _ := os.ReadFile(filepath.Join(home, "starts"))
		return strings.Count(string(data), "start")
	}
	kill := func() {
		data, err := os.ReadFile(filepath.Join(home, "pid"))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		syscall.Kill(pid, syscall.SIGKILL)
	}
	if err := EnsureService(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	waitLocked()
	t.Cleanup(func() { kill() })
	for range 3 {
		if err := EnsureService(context.Background(), home); err != nil {
			t.Fatal(err)
		}
	}
	if got := starts(); got != 1 {
		t.Fatalf("started %d schedulers, want 1", got)
	}
	// An exited scheduler is started again on the next call.
	kill()
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, err := LockService(home)
		if err == nil {
			release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := EnsureService(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	waitLocked()
	if got := starts(); got != 2 {
		t.Fatalf("started %d schedulers, want 2", got)
	}
}

func TestServiceLockIsSingleInstance(t *testing.T) {
	home := t.TempDir()
	release, err := LockService(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockService(home); !errors.Is(err, ErrServiceRunning) {
		t.Fatal("second lock:", err)
	}
	release()
	again, err := LockService(home)
	if err != nil {
		t.Fatal(err)
	}
	again()
}
