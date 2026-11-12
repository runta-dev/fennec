// Package upstream builds the inventory of shuttle behaviours fennec-cua is
// measured against: the upstream test names and release-note entries of the
// pinned release. It reads a local checkout and never copies upstream code.
package upstream

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind names where an inventory item comes from.
type Kind string

const (
	KindNode      Kind = "node"
	KindRust      Kind = "rust"
	KindChangelog Kind = "changelog"
)

// Item is one upstream behaviour that the capability catalog must account for.
type Item struct {
	ID    string // stable reference used in the catalog
	Kind  Kind
	Title string // the test name or release-note headline, for display only
	// MacOS is false when the item only concerns another platform; such items
	// need no catalog entry.
	MacOS bool
	// Why says how MacOS was decided, for review.
	Why string
}

// Pin is the upstream release named in tests/UPSTREAM.
type Pin struct {
	Repo    string
	Commit  string
	Version string
}

// ReadPin parses the UPSTREAM file.
func ReadPin(path string) (Pin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pin{}, err
	}
	var p Pin
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "repo":
			p.Repo = value
		case "commit":
			p.Commit = value
		case "version":
			p.Version = value
		}
	}
	if p.Repo == "" || p.Commit == "" {
		return Pin{}, fmt.Errorf("%s: repo and commit are required", path)
	}
	return p, nil
}

// Checkout returns a checkout of the pinned commit under cacheDir, cloning or
// fetching it when missing.
func Checkout(p Pin, cacheDir string) (string, error) {
	dir := filepath.Join(cacheDir, "shuttle-"+p.Commit[:12])
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		if err == nil && strings.TrimSpace(string(head)) == p.Commit {
			return dir, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	steps := [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", p.Repo, p.Commit},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir, nil
}

// Inventory lists every upstream item of the checkout at root.
func Inventory(root string) ([]Item, error) {
	var items []Item
	node, err := nodeTests(root)
	if err != nil {
		return nil, err
	}
	items = append(items, node...)
	rust, err := rustTests(root)
	if err != nil {
		return nil, err
	}
	items = append(items, rust...)
	notes, err := changelog(root)
	if err != nil {
		return nil, err
	}
	items = append(items, notes...)
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			return nil, fmt.Errorf("duplicate inventory id %q", it.ID)
		}
		seen[it.ID] = true
	}
	return items, nil
}

var (
	nodeTest = regexp.MustCompile(`^\s*test\(\s*(['"` + "`" + `])((?:\\.|[^\\])*?)['"` + "`" + `]\s*,(.*)$`)
	nodeSkip = regexp.MustCompile(`skip:\s*process\.platform\s*(===|!==)\s*'(\w+)'`)
)

func nodeTests(root string) ([]Item, error) {
	files, err := filepath.Glob(filepath.Join(root, "runtime", "test", "*.test.mts"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var items []Item
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(file)
		for _, line := range strings.Split(string(data), "\n") {
			m := nodeTest.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			title := strings.ReplaceAll(m[2], `\'`, `'`)
			mac, why := true, "runs on every platform"
			if s := nodeSkip.FindStringSubmatch(m[3]); s != nil {
				onlyOn := s[1] == "!=="
				if onlyOn {
					mac = s[2] == "darwin"
					why = "runs only on " + s[2]
				} else {
					mac = s[2] != "darwin"
					why = "skipped on " + s[2]
				}
			}
			items = append(items, Item{
				ID:    "node/" + strings.TrimSuffix(base, ".test.mts") + "/" + title,
				Kind:  KindNode,
				Title: title,
				MacOS: mac,
				Why:   why,
			})
		}
	}
	return items, nil
}

// Files whose code serves only another platform.
var otherPlatformRust = map[string]string{
	"crates/desk/src/atspi.rs":      "linux",
	"crates/desk/src/spi.rs":        "linux",
	"crates/desk/src/xshm.rs":       "linux",
	"crates/desk/src/xtest.rs":      "linux",
	"crates/desk/src/noshm.rs":      "linux",
	"crates/desk/src/scope.rs":      "linux",
	"crates/desk/src/uia.rs":        "windows",
	"crates/desk/src/uia_roles.rs":  "windows",
	"crates/desk/src/pipesec.rs":    "windows",
	"crates/shuttle/src/win.rs":     "windows",
	"crates/shuttle/src/android.rs": "android",
}

var (
	rustTestAttr = regexp.MustCompile(`^\s*#\[(?:tokio::)?test\b`)
	rustFn       = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+(\w+)`)
	rustCfg      = regexp.MustCompile(`^\s*#\[cfg\((.*)\)\]`)
)

func rustTests(root string) ([]Item, error) {
	var items []Item
	err := filepath.WalkDir(filepath.Join(root, "crates"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".rs" {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fileOS := otherPlatformRust[rel]
		if strings.HasPrefix(rel, "crates/android/") {
			fileOS = "android"
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !rustTestAttr.MatchString(line) {
				continue
			}
			var cfgs []string
			name := ""
			for j := i - 1; j >= 0 && j >= i-4; j-- {
				if m := rustCfg.FindStringSubmatch(lines[j]); m != nil {
					cfgs = append(cfgs, m[1])
				} else if !strings.HasPrefix(strings.TrimSpace(lines[j]), "#[") {
					break
				}
			}
			for j := i + 1; j < len(lines) && j <= i+6; j++ {
				if m := rustCfg.FindStringSubmatch(lines[j]); m != nil {
					cfgs = append(cfgs, m[1])
				}
				if m := rustFn.FindStringSubmatch(lines[j]); m != nil {
					name = m[1]
					break
				}
			}
			if name == "" {
				return fmt.Errorf("%s:%d: test attribute without a function", rel, i+1)
			}
			mac, why := true, "shared or macOS code"
			if fileOS != "" {
				mac, why = false, "file serves "+fileOS
			}
			for _, c := range cfgs {
				if cfgExcludesMac(c) {
					mac, why = false, "cfg("+c+")"
				}
			}
			items = append(items, Item{
				ID:    "rust/" + strings.TrimPrefix(rel, "crates/") + "::" + name,
				Kind:  KindRust,
				Title: name,
				MacOS: mac,
				Why:   why,
			})
		}
		return nil
	})
	return items, err
}

func cfgExcludesMac(c string) bool {
	c = strings.ReplaceAll(c, " ", "")
	switch c {
	case "windows", `target_os="linux"`, `target_os="windows"`, `target_os="android"`:
		return true
	case `not(target_os="macos")`, "not(unix)":
		return true
	}
	return false
}

var (
	releaseHeading = regexp.MustCompile(`^## (\d+\.\d+\.\d+)\b`)
	noteHeadline   = regexp.MustCompile(`^- \*\*(.+?)\*\*`)
	otherPlatform  = regexp.MustCompile(`^(?:Linux|Windows|Android|X11|AT-SPI|UI Automation)\b[^:]*:`)
)

func changelog(root string) ([]Item, error) {
	data, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return nil, err
	}
	var items []Item
	version := ""
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := releaseHeading.FindStringSubmatch(line); m != nil {
			version, n = m[1], 0
			continue
		}
		if version == "" || !strings.HasPrefix(line, "- ") {
			continue
		}
		n++
		title := strings.TrimPrefix(line, "- ")
		if m := noteHeadline.FindStringSubmatch(line); m != nil {
			title = m[1]
		}
		if len(title) > 160 {
			title = title[:157] + "..."
		}
		mac, why := true, "release note"
		if otherPlatform.MatchString(title) {
			mac, why = false, "release note for another platform"
		}
		items = append(items, Item{
			ID:    fmt.Sprintf("changelog/%s/%d", version, n),
			Kind:  KindChangelog,
			Title: title,
			MacOS: mac,
			Why:   why,
		})
	}
	return items, sc.Err()
}
