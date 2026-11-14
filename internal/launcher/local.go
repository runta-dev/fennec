// Package launcher owns local CLI interception. No original binary is replaced.
package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var Supported = map[string]bool{"claude": true, "codex": true, "pi": true, "opencode": true, "kimi": true}

// ResolveOriginal excludes the shim directory and every alias of the running
// executable, including symlinks and hard links. Relative PATH entries are
// deliberately excluded to avoid executing a project-local impersonator.
func ResolveOriginal(agent, path, shimDir, self string) (string, error) {
	if !Supported[agent] {
		return "", fmt.Errorf("unsupported agent %q", agent)
	}
	selfInfo, _ := os.Stat(self)
	shim, _ := filepath.EvalSymlinks(shimDir)
	for _, directory := range filepath.SplitList(path) {
		if !filepath.IsAbs(directory) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil {
			continue
		}
		if resolved == shim || filepath.Clean(directory) == filepath.Clean(shimDir) {
			continue
		}
		candidate := filepath.Join(directory, agent)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			continue
		}
		if selfInfo != nil && os.SameFile(selfInfo, info) {
			continue
		}
		original, err := OriginalEntry(candidate)
		if errors.Is(err, errManagedLauncher) {
			continue
		}
		return original, err
	}
	return "", fmt.Errorf("original %s executable not found outside Fennec shims", agent)
}

func ExecLocal(agent string, args []string, shimDir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	original, err := ResolveOriginal(agent, os.Getenv("PATH"), shimDir, self)
	if err != nil {
		return err
	}
	// exec replaces the launcher: terminal, signal behavior, cwd and exit code
	// remain the original CLI's rather than being reimplemented in a wrapper.
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "FENNEC_LAUNCH_MODE=") {
			env = append(env, value)
		}
	}
	return syscall.Exec(original, append([]string{original}, args...), env)
}
