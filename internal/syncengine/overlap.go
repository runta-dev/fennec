package syncengine

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WithRegistryLock serializes registration and session creation, so two callers
// cannot both pass the overlap check before either registers its session.
func WithRegistryLock(ctx context.Context, directory string, action func() error) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return action()
}
func CheckOverlap(directory string, desired Registration) error {
	entries, err := ListProjects(directory)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(desired.Root)
	if err != nil {
		return err
	}
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	for _, entry := range entries {
		existing, err := filepath.EvalSymlinks(entry.Root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if existing == root && entry.Session == desired.Session && entry.DataDirectory == desired.DataDirectory {
			continue
		}
		if contains(existing, root) || contains(root, existing) {
			return fmt.Errorf("project overlaps registered synchronization %s; reuse or pause and remove that mapping first", entry.Session)
		}
	}
	return nil
}
