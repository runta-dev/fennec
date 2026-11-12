package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Read under the same lock used by Magpie configuration writers, so an unchanged
// route is accepted only from a completed write, never an in-progress file edit.
func readModelWithLock(ctx context.Context, home, agent string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	directory := filepath.Join(home, ".local/fennec/install-locks")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, "magpie-config.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if output, ok := readConfiguredMagpieModelOutput(home, agent); ok {
		return output, nil
	}
	return exec.CommandContext(ctx, "magpie", agent, "model").Output()
}
