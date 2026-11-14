// Package clipboardbridge exposes a single-use clipboard to one cloud session.
// Kimi reads the wl-paste interface before creating its native image attachment.
package clipboardbridge

import (
	"context"
	"errors"
	"fennec/internal/clipboard"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

const SessionEnv = "FENNEC_CLIPBOARD_SESSION"
const Label = "fennec.clipboard"
const Version = "wl-paste-v1"

func directory(home, id string) (string, error) {
	if !filepath.IsAbs(home) || !sessionid.Valid(id) {
		return "", fmt.Errorf("invalid clipboard session")
	}
	return filepath.Join(home, ".local/fennec/clipboard", id), nil
}

func Bin(home, id string) (string, error) {
	dir, err := directory(home, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin"), nil
}

// Prepare installs a private adapter, without changing the user's global PATH
// or clipboard. binary is the stable fennec-terminal executable.
func Prepare(home, id, binary string) error {
	dir, err := directory(home, id)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(binary) {
		return fmt.Errorf("clipboard adapter must be absolute")
	}
	for _, path := range []string{filepath.Dir(dir), dir, filepath.Join(dir, "bin")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("clipboard directory must be private")
		}
	}
	for _, name := range []string{"wl-paste", "xclip"} {
		link := filepath.Join(dir, "bin", name)
		if target, err := os.Readlink(link); err == nil && target == binary {
			continue
		}
		if err := os.Symlink(binary, link); err != nil {
			return err
		}
	}
	return nil
}

// Deliver serializes images for the same session and waits for the native
// clipboard reader to consume this one before allowing later input through.
// A menu that ignores Ctrl-V cannot leave a stale image for the next paste.
func Deliver(ctx context.Context, home, id, path string, trigger func() error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dir, err := directory(home, id)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "paste.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	file, err := os.CreateTemp(dir, "image-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	n, copyErr := io.Copy(file, io.LimitReader(source, clipboard.MaxImageBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || n == 0 || n > clipboard.MaxImageBytes {
		return fmt.Errorf("could not stage clipboard image")
	}
	ref, err := os.CreateTemp(dir, "reference-")
	if err != nil {
		return err
	}
	defer os.Remove(ref.Name())
	_, writeErr := io.WriteString(ref, filepath.Base(file.Name()))
	closeErr = ref.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("could not publish clipboard image")
	}
	current := filepath.Join(dir, "current")
	if err := os.Rename(ref.Name(), current); err != nil {
		return err
	}
	defer os.Remove(current)
	if err := trigger(); err != nil {
		return err
	}
	for {
		if _, err := os.Lstat(file.Name()); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Read implements only Kimi's read-only wl-paste requests. Type probes never
// consume the image; a complete PNG read acknowledges consumption to Deliver.
func Read(home, id string, args []string, output io.Writer) error {
	dir, err := directory(home, id)
	if err != nil {
		return err
	}
	list := len(args) == 1 && args[0] == "--list-types"
	read := len(args) == 3 && args[0] == "--type" && args[1] == "image/png" && args[2] == "--no-newline"
	if !list && !read {
		return fmt.Errorf("unsupported clipboard query")
	}
	ref, err := os.OpenFile(filepath.Join(dir, "current"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	name, err := io.ReadAll(io.LimitReader(ref, 128))
	ref.Close()
	if err != nil || !regexp.MustCompile(`^image-[0-9]+$`).Match(name) {
		return fmt.Errorf("invalid clipboard reference")
	}
	path := filepath.Join(dir, string(name))
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > clipboard.MaxImageBytes {
		return fmt.Errorf("invalid clipboard image")
	}
	if list {
		_, err = io.WriteString(output, "image/png\n")
		return err
	}
	if _, err := io.Copy(output, file); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// XclipArgs keeps Kimi's file-clipboard fallback inside this session too.
func XclipArgs(args []string) []string {
	if len(args) == 5 && args[0] == "-selection" && args[1] == "clipboard" && args[2] == "-t" && args[4] == "-o" {
		if args[3] == "TARGETS" {
			return []string{"--list-types"}
		}
		if args[3] == "image/png" {
			return []string{"--type", "image/png", "--no-newline"}
		}
	}
	return nil
}
