package cli

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestStopLockExcludesConcurrentReconnectUntilReleased(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	stopLock, err := acquireControlStartLock(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer stopLock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if lock, err := acquireControlStartLock(ctx, directory); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("reconnect entered while stop held lock: %v", err)
	}
	stopLock.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	lock, err := acquireControlStartLock(ctx2, directory)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
}
