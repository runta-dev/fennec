package syncengine

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ProjectIgnores applies root Git patterns to both Mutagen endpoints. Patterns
// remain patterns, so artifacts created after initial sync are also excluded.
// Git constructs without verified Mutagen equivalence fail closed.
func ProjectIgnores(root string) ([]string, error) {

	paths := []string{".gitignore"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gitBinary, gitErr := exec.LookPath("git")
	if gitErr == nil && runtime.GOOS == "darwin" && filepath.Clean(gitBinary) == "/usr/bin/git" {
		if exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p").Run() != nil {
			gitErr = fmt.Errorf("command line tools not installed")
		}
	}
	if gitErr == nil {
		command := exec.CommandContext(ctx, gitBinary, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".gitignore", "**/.gitignore")
		if output, err := command.Output(); err == nil {
			seen := map[string]bool{".gitignore": true}
			for _, name := range strings.Split(string(output), "\x00") {
				if name == "" || seen[name] {
					continue
				}
				if filepath.IsAbs(name) || strings.HasPrefix(filepath.Clean(name), "..") {
					return nil, fmt.Errorf("invalid ignore file path")
				}
				seen[name] = true
				paths = append(paths, name)
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	sort.Strings(paths)
	rules := []string{}
	for _, name := range paths {
		parsed, err := ignoreFileRules(root, name)
		if err != nil {
			return nil, err
		}
		rules = append(rules, parsed...)
	}
	return append(rules, defaultIgnores...), nil
}

func ignoreFileRules(root, name string) ([]string, error) {
	rules := []string{}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return rules, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("ignore file must be regular: %s", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scope := filepath.ToSlash(filepath.Dir(name))
	scan := bufio.NewScanner(file)
	for line := 1; scan.Scan(); line++ {
		pattern := scan.Text()
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		if strings.ContainsAny(pattern, "\\\r") || strings.TrimSpace(pattern) != pattern || pattern == "!" {
			return nil, fmt.Errorf(".gitignore line %d requires unsupported escaping or whitespace semantics", line)
		}
		// Negation differs when an ancestor directory is ignored. Never widen sync
		// by guessing at Git's traversal behavior.
		if strings.HasPrefix(pattern, "!") {
			return nil, fmt.Errorf(".gitignore line %d uses negation requiring Git-compatible traversal", line)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
		if inner == "" {
			return nil, fmt.Errorf("invalid .gitignore line %d", line)
		}
		if strings.Contains(inner, "/") && !strings.HasPrefix(pattern, "/") {
			pattern = "/" + pattern
		}
		if scope != "." {
			if strings.HasPrefix(pattern, "/") {
				pattern = "/" + scope + pattern
			} else {
				pattern = "/" + scope + "/**/" + pattern
			}
		}
		rules = append(rules, pattern)
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	// Baseline exclusions cannot be undone by project configuration.
	return rules, nil
}
