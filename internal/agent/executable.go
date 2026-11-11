package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// originalExecutable never runs a launcher to discover what it wraps.
func originalExecutable(home, candidate string) bool {
	info, err := os.Stat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false
	}
	shim := filepath.Join(home, ".local/share/fennec/shims")
	if dir, err := filepath.EvalSymlinks(shim); err == nil && filepath.Dir(resolved) == dir {
		return false
	}
	if self, err := os.Executable(); err == nil {
		if executable, err := os.Stat(self); err == nil && os.SameFile(info, executable) {
			return false
		}
	}
	file, err := os.Open(candidate)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, 128)
	n, err := file.Read(header)
	if err != nil && err != io.EOF {
		return false
	}
	return !bytes.Contains(header[:n], []byte("# Fennec managed launcher v1\n"))
}

// EntryPath returns the PATH entry itself, not its resolved package binary.
func EntryPath(id string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, dir := range searchPaths(home) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, id)
		if originalExecutable(home, candidate) {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

// ManagedEntryPaths includes inactive toolchain versions, not only the first
// executable currently on PATH. Sidecars identify entries owned by Fennec.
func ManagedEntryPaths(home string) []string {
	seen := map[string]bool{}
	var result []string
	for _, dir := range searchPaths(home) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, id := range []string{"claude", "codex", "pi", "opencode"} {
			path := filepath.Join(dir, id)
			if seen[path] {
				continue
			}
			seen[path] = true
			if _, err := os.Lstat(path + ".fennec-original.json"); err == nil {
				result = append(result, path)
			}
		}
	}
	return result
}
