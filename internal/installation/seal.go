package installation

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Bind signature verification to the exact private tree, including contents,
// links, metadata and extended attributes (which can hold code signatures).
// Timestamps alone are insufficient: rapid writes can share a filesystem tick.
// The proof lives only in this process and never skips first verification.
type sealedEntry struct {
	Stat                syscall.Stat_t
	Content, Attributes [32]byte
	Link                string
}
type treeSeal map[string]sealedEntry

func sealTree(root string) (treeSeal, error) {
	seal := treeSeal{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if path == root && !info.IsDir() {
			return fmt.Errorf("private application directory required")
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported application entry")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("kernel file identity unavailable")
		}
		value := *stat
		clearAccessTime(&value)
		item := sealedEntry{Stat: value}
		if info.Mode().IsRegular() {
			file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) {
				file.Close()
				return fmt.Errorf("application entry changed during verification")
			}
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			after, statErr := file.Stat()
			closed := file.Close()
			if err != nil {
				return err
			}
			if closed != nil {
				return closed
			}
			if statErr != nil {
				return statErr
			}
			final := *after.Sys().(*syscall.Stat_t)
			clearAccessTime(&final)
			if !reflect.DeepEqual(value, final) {
				return fmt.Errorf("application entry changed during verification")
			}
			copy(item.Content[:], hash.Sum(nil))
		} else if info.Mode()&os.ModeSymlink != 0 {
			item.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		item.Attributes, err = attributeDigest(path)
		if err != nil {
			return err
		}
		seal[path] = item
		return nil
	})
	return seal, err
}

func attributeDigest(path string) ([32]byte, error) {
	var result [32]byte
	size, err := unix.Llistxattr(path, nil)
	if err != nil {
		return result, err
	}
	data := make([]byte, size)
	n, err := unix.Llistxattr(path, data)
	if err != nil {
		return result, err
	}
	names := strings.Split(strings.TrimSuffix(string(data[:n]), "\x00"), "\x00")
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		if name == "" {
			continue
		}
		size, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			return result, err
		}
		value := make([]byte, size)
		n, err := unix.Lgetxattr(path, name, value)
		if err != nil {
			return result, err
		}
		binary.Write(hash, binary.BigEndian, uint64(len(name)))
		hash.Write([]byte(name))
		binary.Write(hash, binary.BigEndian, uint64(n))
		hash.Write(value[:n])
	}
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (seal treeSeal) matches(root string) bool {
	defer launchprofile.Begin("update.check_candidate")()
	current, err := sealTree(root)
	return err == nil && reflect.DeepEqual(seal, current)
}

func verifyTree(ctx context.Context, root string, verify func(context.Context, string) error) (treeSeal, error) {
	seal, err := sealTree(root)
	if err != nil {
		return nil, err
	}
	if err := verify(ctx, root); err != nil {
		return nil, ErrUnverifiedApplication
	}
	if !seal.matches(root) {
		return nil, ErrUnverifiedApplication
	}
	return seal, nil
}
