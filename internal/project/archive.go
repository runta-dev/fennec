package project

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WriteArchive creates a source-only snapshot from a previously inspected
// manifest. Callers must discard partial output on error and publish only after
// success; no .git metadata, symlinks or unlisted files enter the archive.
func WriteArchive(ctx context.Context, manifest Manifest, output io.Writer) error {
	root, err := filepath.EvalSymlinks(manifest.Root)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("absolute project root required")
	}
	writer := tar.NewWriter(output)
	seen := map[string]bool{}
	var total int64
	if len(manifest.Files) > 20000 {
		return fmt.Errorf("too many files")
	}
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.FromSlash(entry.Path)
		clean := filepath.Clean(name)
		if name == "" || clean == "." || clean != name || filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || sensitive(name) || seen[name] {
			return fmt.Errorf("invalid archive path")
		}
		seen[name] = true
		if entry.Size < 0 || entry.Size > MaxFileBytes || total+entry.Size > MaxProjectBytes {
			return fmt.Errorf("archive size limit exceeded")
		}
		full := filepath.Join(root, name)
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil || resolved != full {
			return fmt.Errorf("project path changed")
		}
		before, err := os.Lstat(full)
		if err != nil || !before.Mode().IsRegular() {
			return fmt.Errorf("project file unavailable")
		}
		file, err := os.Open(full)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil || !os.SameFile(before, info) || info.Size() != entry.Size {
			file.Close()
			return fmt.Errorf("project file changed")
		}
		// Buffer at most one bounded file, so bytes verified are exactly bytes written.
		data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
		file.Close()
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		if int64(len(data)) != entry.Size || hex.EncodeToString(hash[:]) != entry.SHA256 {
			return fmt.Errorf("project content changed since manifest")
		}
		if (info.Mode()&0111 != 0) != entry.Executable {
			return fmt.Errorf("project executable mode changed")
		}
		mode := int64(0600)
		if entry.Executable {
			mode = 0700
		}
		if err = writer.WriteHeader(&tar.Header{Name: entry.Path, Mode: mode, Size: entry.Size, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err = writer.Write(data); err != nil {
			return err
		}
		total += entry.Size
	}
	if total != manifest.Bytes {
		return fmt.Errorf("manifest byte count mismatch")
	}
	return writer.Close()
}
