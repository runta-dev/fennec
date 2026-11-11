package installation

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os/exec"
)

func readOnlyVolume(path string) (bool, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return stat.Flags&unix.MNT_RDONLY != 0, err
}
func writableDirectory(path string) bool { return unix.Access(path, unix.W_OK|unix.X_OK) == nil }
func SystemOperations() Operations {
	return Operations{
		Verify: func(ctx context.Context, path string) error {
			return exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", path).Run()
		},
		Copy: func(ctx context.Context, source, destination string) error {
			return exec.CommandContext(ctx, "/usr/bin/ditto", source, destination).Run()
		},
		Publish: func(source, destination string) error {
			if err := unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL); err != nil {
				return fmt.Errorf("publish application: %w", err)
			}
			return nil
		},
	}
}
