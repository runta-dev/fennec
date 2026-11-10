package agent

import (
	"context"
	"fennec/internal/launcher"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// VersionInfo never represents an unverified version as installable.
type VersionInfo struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

var semver = regexp.MustCompile(`(?:^|\s)v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)`)

func parseVersion(output string) (string, error) {
	match := semver.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("unrecognized version output")
	}
	return match[1], nil
}

type limitedOutput struct{ text []byte }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 8192 - len(b.text)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.text = append(b.text, p...)
	}
	return n, nil
}

// DetectVersions executes only the supported CLI's --version, with bounded
// output/time. It never reads configuration or login directories.
func DetectVersions(ctx context.Context) []VersionInfo {
	home, _ := os.UserHomeDir()
	paths := searchPaths(home)
	var result []VersionInfo
	for _, definition := range catalog {
		if !definition.cloud {
			continue
		}
		id := definition.id
		var path string
		var err error
		for _, dir := range paths {
			if dir == "" || !filepath.IsAbs(dir) {
				continue
			}
			candidate := filepath.Join(dir, id)
			if originalExecutable(home, candidate) {
				path, err = launcher.OriginalEntry(candidate)
				if err != nil {
					continue
				}
				break
			}
		}
		if path == "" {
			continue
		}
		item := VersionInfo{ID: id, Path: path}
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		command := exec.CommandContext(runCtx, path, "--version")
		command.WaitDelay = time.Second
		command.Env = append(os.Environ(), "PATH="+strings.Join(paths, string(os.PathListSeparator)))
		output := &limitedOutput{}
		command.Stdout = output
		command.Stderr = output
		err = command.Run()
		cancel()
		if err != nil {
			item.Error = "version detection failed"
		} else {
			item.Version, err = parseVersion(strings.TrimSpace(string(output.text)))
			if err != nil {
				item.Error = err.Error()
			}
		}
		result = append(result, item)
	}
	return result
}

// VersionAt reads the explicitly resolved executable without searching another PATH.
func VersionAt(ctx context.Context, path string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || !originalExecutable(home, path) {
		return "", fmt.Errorf("invalid agent executable")
	}
	path, err = launcher.OriginalEntry(path)
	if err != nil {
		return "", err
	}
	step, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(step, path, "--version")
	cmd.WaitDelay = time.Second
	output := &limitedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("agent version detection failed: %w", err)
	}
	return parseVersion(string(output.text))
}
