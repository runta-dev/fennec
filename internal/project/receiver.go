package project

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var snapshotID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var digestID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func safeSnapshotPath(name string) bool {
	return name != "" && name != "." && !path.IsAbs(name) && path.Clean(name) == name && !strings.ContainsRune(name, 0) && name != ".." && !strings.HasPrefix(name, "../")
}

// Receive validates a manifest and a complete tar archive before atomically
// publishing a fresh snapshot. It never overwrites existing cloud edits.
func Receive(ctx context.Context, home, id string, input io.Reader) (UploadResult, error) {
	var result UploadResult
	if !snapshotID.MatchString(id) || !filepath.IsAbs(home) {
		return result, fmt.Errorf("invalid snapshot destination")
	}
	reader := bufio.NewReaderSize(input, (4<<20)+1)
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 4<<20 {
		return result, fmt.Errorf("invalid project manifest")
	}
	var manifest Manifest
	if json.Unmarshal(line, &manifest) != nil || len(manifest.Files) > 20000 {
		return result, fmt.Errorf("invalid project manifest")
	}
	expected := make(map[string]File, len(manifest.Files))
	var total int64
	for _, entry := range manifest.Files {
		_, duplicate := expected[entry.Path]
		if !safeSnapshotPath(entry.Path) || duplicate || entry.Size < 0 || entry.Size > MaxFileBytes || !digestID.MatchString(entry.SHA256) {
			return result, fmt.Errorf("invalid project file manifest")
		}
		total += entry.Size
		if total > MaxProjectBytes {
			return result, fmt.Errorf("project size exceeds limit")
		}
		expected[entry.Path] = entry
	}
	if total != manifest.Bytes || manifest.WorkingDirectory != "." && !safeSnapshotPath(manifest.WorkingDirectory) {
		return result, fmt.Errorf("invalid project manifest totals or working directory")
	}
	base := filepath.Join(home, ".local/fennec/projects")
	if err = os.MkdirAll(base, 0700); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(base, ".receiving-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	// Include a bounded allowance for tar headers, padding and PAX path records.
	limited := &io.LimitedReader{R: reader, N: MaxProjectBytes + int64(len(expected))*8192 + 1024}
	archive := tar.NewReader(limited)
	seen := make(map[string]bool, len(expected))
	for {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		before := limited.N
		member, nextErr := archive.Next()
		if nextErr == io.EOF {
			// Go's tar reader also accepts bare EOF. Require both end blocks so a
			// disconnected upload cannot publish even when its last file was complete.
			if before-limited.N < 1024 {
				return result, fmt.Errorf("truncated project archive")
			}
			break
		}
		if nextErr != nil {
			return result, fmt.Errorf("invalid project archive: %w", nextErr)
		}
		entry, ok := expected[member.Name]
		if !ok || seen[member.Name] || member.Typeflag != tar.TypeReg || member.Size != entry.Size {
			return result, fmt.Errorf("unexpected project archive member")
		}
		destination := filepath.Join(stage, filepath.FromSlash(member.Name))
		if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return result, err
		}
		mode := os.FileMode(0600)
		if entry.Executable {
			mode = 0700
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return result, err
		}
		digest := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(out, digest), archive, entry.Size)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
			return result, fmt.Errorf("project content verification failed")
		}
		seen[member.Name] = true
	}
	if len(seen) != len(expected) {
		return result, fmt.Errorf("missing project archive members")
	}
	if err = os.MkdirAll(filepath.Join(stage, filepath.FromSlash(manifest.WorkingDirectory)), 0700); err != nil {
		return result, err
	}
	// Serialize publishers by identity, including other processes. Keep the lock
	// inode after unlock so a second uploader cannot acquire an unrelated inode.
	lock, err := os.OpenFile(filepath.Join(base, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	for {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return result, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	target := filepath.Join(base, id)
	if _, err = os.Lstat(target); err == nil {
		return result, fmt.Errorf("snapshot already exists; preserve cloud edits")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = os.Rename(stage, target); err != nil {
		return result, err
	}
	return UploadResult{Directory: filepath.Join(target, filepath.FromSlash(manifest.WorkingDirectory)), Files: len(seen), Bytes: total}, nil
}
