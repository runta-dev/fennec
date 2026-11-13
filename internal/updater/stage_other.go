//go:build !darwin

package updater

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func isRosettaTranslated() bool { return false }

func isReadOnlyFilesystem(_ string) bool { return false }

func unpackArchive(_ context.Context, zipPath, destDir string) error {
	return unpackZipPortable(zipPath, destDir)
}

func unregisterLaunchServices(_ string) {}

func VerifySameSigner(_ context.Context, _, _ string) error {
	return fmt.Errorf("macOS signature verification requires darwin")
}

// cloneTreeSystem copies src to dst: directories, regular files with their
// permission bits, and symlinks.
func cloneTreeSystem(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.Mkdir(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		default:
			return fmt.Errorf("unsupported file type at %s", rel)
		}
	})
}

func clearQuarantine(string) {}
