package cli

import (
	"context"
	"fennec/internal/launchprofile"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Start, replacement and explicit stop share this lock. Stop keeps it until
// launchd has removed the retiring registration, including for older CLI clients
// that automatically reconnect using the same startup lock file.
func acquireControlStartLock(ctx context.Context, directory string) (*os.File, error) {
	defer launchprofile.Begin("daemon.start_lock")()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private control directory required")
	}
	file, err := os.OpenFile(filepath.Join(directory, "control-start.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
