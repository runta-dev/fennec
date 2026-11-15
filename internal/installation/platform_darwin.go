package installation

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func clearAccessTime(stat *syscall.Stat_t) { stat.Atimespec = syscall.Timespec{} }

func readOnlyVolume(path string) (bool, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return stat.Flags&unix.MNT_RDONLY != 0, err
}
func writableDirectory(path string) bool { return unix.Access(path, unix.W_OK|unix.X_OK) == nil }
func SystemOperations() Operations {
	return Operations{
		StopUI: func(ctx context.Context, incoming, destination string) (bool, error) {
			adapter := filepath.Join(incoming, "Contents/MacOS/Fennec")
			// The installed daemon's own signed adapter is already running on
			// this Mac. Do not cold-start the incoming UI merely to stop the old
			// one. Standalone installers keep using their verified incoming copy.
			if executable, err := os.Executable(); err == nil {
				parent, parentErr := filepath.EvalSymlinks(filepath.Dir(executable))
				target, targetErr := filepath.EvalSymlinks(filepath.Join(destination, "Contents/MacOS"))
				if parentErr == nil && targetErr == nil && parent == target {
					adapter = filepath.Join(parent, "Fennec")
				}
			}
			raw, runErr := exec.CommandContext(ctx, adapter, "--platform-stop-ui", destination).Output()
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
			if err := ctx.Err(); err != nil {
				return err
			}
			// APFS clones preserve signatures and extended attributes without
			// copying Chromium's hundreds of megabytes before every update.
			if err := unix.Clonefile(source, destination, unix.CLONE_NOFOLLOW); err == nil {
				return nil
			}
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
