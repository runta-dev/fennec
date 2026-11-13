package setup

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// fileSignature identifies one version of a file without reading it.
type fileSignature struct {
	exists        bool
	inode, size   int64
	modified, chg int64
}

func statSignature(path string) fileSignature {
	info, err := os.Lstat(path)
	if err != nil {
		return fileSignature{}
	}
	signature := fileSignature{exists: true, size: info.Size(), modified: info.ModTime().UnixNano()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		signature.inode = int64(stat.Ino)
		signature.chg = statChangeNanos(stat)
	}
	return signature
}

// kernelWake delivers a wake-up whenever the kernel reports activity on the
// watched files or their directories (kqueue on macOS, inotify on Linux).
// Implementations stop when ctx ends and may return nil when unavailable.
var kernelWake = platformKernelWake

// WatchFiles signals when any of paths is created, written, replaced or
// removed. It never reads file contents. Kernel notifications make the signal
// prompt; a stat check every poll covers anything they miss, including
// directories that appear later. Signatures are taken before WatchFiles
// returns, so a change made after it returns is never missed.
func WatchFiles(ctx context.Context, paths []string, poll time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)
	signatures := make([]fileSignature, len(paths))
	for i, path := range paths {
		signatures[i] = statSignature(path)
	}
	wake := kernelWake(ctx, paths)
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-ticker.C:
			}
			changed := false
			for i, path := range paths {
				if current := statSignature(path); current != signatures[i] {
					signatures[i] = current
					changed = true
				}
			}
			if changed {
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}

// watchDirectories returns the distinct parent directories of paths.
func watchDirectories(paths []string) []string {
	seen := map[string]bool{}
	var directories []string
	for _, path := range paths {
		directory := filepath.Dir(path)
		if !seen[directory] {
			seen[directory] = true
			directories = append(directories, directory)
		}
	}
	return directories
}
