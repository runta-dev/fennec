package cloudimage

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// seedTools places Node.js from the image into the user's home on the first
// start of a volume, where Fennec's preparation expects it
// (~/.local/fennec/node). It never replaces anything already there: an
// existing, newer or user-managed installation always wins, and Fennec's own
// version check downloads the version it pins when the seed does not match.
func seedTools(l Layout) error {
	if l.Seed == "" {
		return nil
	}
	source := filepath.Join(l.Seed, "node")
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return nil
	}
	base := filepath.Join(l.Home, ".local", "fennec")
	target := filepath.Join(base, "node")
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, dir := range []string{filepath.Join(l.Home, ".local"), base} {
		if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := l.chown(dir); err != nil {
			return err
		}
	}
	// Copy into a staging directory and rename, so an interrupted start never
	// leaves a partial installation that looks complete.
	staging, err := os.MkdirTemp(base, ".node-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := copyTree(l, source, staging); err != nil {
		return err
	}
	return os.Rename(staging, target)
}

func copyTree(l Layout, source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			if relative != "." {
				if err := os.Mkdir(destination, mode.Perm()); err != nil {
					return err
				}
			} else if err := os.Chmod(destination, mode.Perm()); err != nil {
				return err
			}
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, destination); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := copyFile(path, destination, mode.Perm()); err != nil {
				return err
			}
		default:
			return nil
		}
		return l.chown(destination)
	})
}

func copyFile(source, target string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
