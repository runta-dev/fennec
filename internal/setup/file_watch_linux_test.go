//go:build linux

package setup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFileWatchArmsBeforeReturning(t *testing.T) {
	// With one scheduler thread an asynchronous arm often runs only after the
	// replacement. That edit must still notify without the long poll fallback.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	for range 10 {
		path := filepath.Join(t.TempDir(), "value")
		if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		changes := WatchFiles(ctx, []string{path}, time.Hour)
		if err := os.WriteFile(path+".next", []byte("after"), 0600); err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := os.Rename(path+".next", path); err != nil {
			cancel()
			t.Fatal(err)
		}
		select {
		case <-changes:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("immediate replacement was lost before watch registration")
		}
		cancel()
	}
}
