package installation

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os/exec"
	"path/filepath"
)

func readOnlyVolume(path string) (bool, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return stat.Flags&unix.MNT_RDONLY != 0, err
}
func writableDirectory(path string) bool { return unix.Access(path, unix.W_OK|unix.X_OK) == nil }
func SystemOperations() Operations {
	return Operations{
		StopUI: func(ctx context.Context, incoming, destination string) (bool, error) {
			raw, runErr := exec.CommandContext(ctx, filepath.Join(incoming, "Contents/MacOS/Fennec"), "--platform-stop-ui", destination).Output()
			var reply struct {
				Stopped bool `json:"stopped"`
				Success bool `json:"success"`
			}
			if len(raw) > 4096 || json.Unmarshal(raw, &reply) != nil {
				return false, fmt.Errorf("unable to verify old UI shutdown")
			}
			if runErr != nil || !reply.Success {
				return reply.Stopped, fmt.Errorf("old UI could not be stopped")
			}
			return reply.Stopped, nil
		},
		OpenUI: func(ctx context.Context, path string) error {
			return exec.CommandContext(ctx, "/usr/bin/open", "-n", path).Run()
		},
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
