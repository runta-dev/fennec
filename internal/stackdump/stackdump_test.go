//go:build unix

package stackdump

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGUSR1 writes the stacks and leaves the process running.
func TestSignalWritesGoroutineDump(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	Install(ctx, dir)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := filepath.Glob(filepath.Join(dir, "goroutines-*.txt"))
		if len(files) == 1 {
			data, err := os.ReadFile(files[0])
			info, _ := os.Stat(files[0])
			if err == nil && strings.Contains(string(data), "TestSignalWritesGoroutineDump") && info.Mode().Perm() == 0o600 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no goroutine dump was written")
}
