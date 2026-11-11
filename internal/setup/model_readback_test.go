package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestModelReadbackWaitsForConfigurationWriter(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".local/fennec/install-locks")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(directory, "magpie-config.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := readModelWithLock(ctx, home, "codex"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read bypassed active writer: %v", err)
	}
}
