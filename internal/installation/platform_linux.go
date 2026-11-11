package installation

import (
	"golang.org/x/sys/unix"
)

func readOnlyVolume(path string) (bool, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return stat.Flags&unix.ST_RDONLY != 0, err
}
func writableDirectory(path string) bool { return unix.Access(path, unix.W_OK|unix.X_OK) == nil }
func SystemOperations() Operations {
	return Operations{Publish: func(source, destination string) error {
		return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	}}
}
