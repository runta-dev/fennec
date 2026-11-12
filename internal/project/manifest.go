// Package project prepares bounded, reviewable project snapshots for cloud launch.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type File struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable"`
}
type Exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Manifest struct {
	Root             string      `json:"root"`
	WorkingDirectory string      `json:"working_directory"`
	Files            []File      `json:"files"`
	Excluded         []Exclusion `json:"excluded"`
	Bytes            int64       `json:"bytes"`
}

const MaxFileBytes int64 = 16 << 20
const MaxProjectBytes int64 = 128 << 20

func sensitive(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".ssh" || lower == ".aws" || lower == ".gnupg" || lower == ".env" || strings.HasPrefix(lower, ".env.") || lower == ".npmrc" || lower == ".netrc" || lower == "id_rsa" || lower == "id_ed25519" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
			return true
		}
	}
	return false
}

// ManifestGit uses Git's own ignore rules but reads current working-tree bytes,
// including dirty tracked and nonignored untracked files. It does not upload.
func ManifestGit(ctx context.Context, cwd string) (Manifest, error) {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return Manifest{}, err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return Manifest{}, err
	}
	gitBinary, err := resolveUsableGit(ctx)
	if err != nil {
		return Manifest{}, err
	}
	cmd := exec.CommandContext(ctx, gitBinary, "-C", real, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return Manifest{}, fmt.Errorf("cloud handoff requires a Git project")
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return Manifest{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Manifest{}, err
	}
	if err = ValidateRoot(root, home); err != nil {
		return Manifest{}, err
	}
	relative, err := filepath.Rel(root, real)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return Manifest{}, fmt.Errorf("working directory escapes project")
	}
	m := Manifest{Root: root, WorkingDirectory: filepath.ToSlash(relative), Files: []File{}, Excluded: []Exclusion{}}
	cmd = exec.CommandContext(ctx, gitBinary, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	output, err = cmd.Output()
	if err != nil {
		return Manifest{}, fmt.Errorf("cannot enumerate Git project")
	}
	names := strings.Split(string(output), "\x00")
	sort.Strings(names)
	seen := map[string]bool{}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if len(seen) > 20000 {
			return m, fmt.Errorf("project file count exceeds 20000")
		}
		clean := filepath.Clean(name)
		if filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return m, fmt.Errorf("invalid project path")
		}
		if sensitive(name) {
			m.Excluded = append(m.Excluded, Exclusion{name, "sensitive_path"})
			continue
		}
		full := filepath.Join(root, clean)
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return m, err
		}
		if !info.Mode().IsRegular() {
			m.Excluded = append(m.Excluded, Exclusion{name, "non_regular_file"})
			continue
		}
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			return m, err
		}
		if resolved != full {
			m.Excluded = append(m.Excluded, Exclusion{name, "symlink_path"})
			continue
		}
		if info.Size() > MaxFileBytes || m.Bytes+info.Size() > MaxProjectBytes {
			return m, fmt.Errorf("project transfer exceeds size limit")
		}
		file, err := os.Open(full)
		if err != nil {
			return m, err
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			file.Close()
			return m, fmt.Errorf("project file changed during inspection")
		}
		hash := sha256.New()
		size, err := io.Copy(hash, io.LimitReader(file, MaxFileBytes+1))
		after, statErr := file.Stat()
		file.Close()
		if err != nil || statErr != nil || size != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return m, fmt.Errorf("project file changed while hashing")
		}
		m.Files = append(m.Files, File{filepath.ToSlash(clean), size, hex.EncodeToString(hash.Sum(nil)), info.Mode()&0111 != 0})
		m.Bytes += size
	}
	return m, nil
}

// On a clean Mac without Command Line Tools, /usr/bin/git is an xcrun shim that
// triggers a GUI installer dialog when executed. Check xcode-select -p first.
func resolveUsableGit(ctx context.Context) (string, error) {
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("Git is not installed on this Mac")
	}
	if runtime.GOOS == "darwin" && filepath.Clean(gitBinary) == "/usr/bin/git" {
		if exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p").Run() != nil {
			return "", fmt.Errorf("Git requires macOS Command Line Tools")
		}
	}
	return gitBinary, nil
}
