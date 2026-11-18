package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// ErrSelfUpdatingEntry reports an entry Fennec leaves to the agent's updater.
// Claude Code's native installer and Cursor's installer keep each version
// under a versions/ directory and update by repointing the entry symlink. A
// wrapper in its place pinned the version found at installation: the updater
// downloaded new versions it could not switch to, and removal restored the
// stale link beside newer installed versions, so Claude Code reported the
// mismatch at every start. The shell functions and shims cover these agents.
var ErrSelfUpdatingEntry = errors.New("entry is switched by the agent's own updater; covered by shell integration")

// versionStore splits a target inside a versions/ directory into the store,
// the version component and the path below it.
func versionStore(target string) (store, version, rest string, ok bool) {
	parts := strings.Split(filepath.Clean(target), string(filepath.Separator))
	for i := len(parts) - 2; i > 0; i-- {
		if parts[i] == "versions" {
			return strings.Join(parts[:i+1], string(filepath.Separator)), parts[i+1], filepath.Join(parts[i+2:]...), true
		}
	}
	return "", "", "", false
}

// newestVersion is the newest version in store with an executable at rest,
// comparing the numeric runs of the names (2.1.327 > 2.1.318,
// 2026.11.05-86047df > 2026.10.30-…), or "" when none qualifies.
func newestVersion(store, rest string) string {
	entries, err := os.ReadDir(store)
	if err != nil {
		return ""
	}
	best := ""
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(store, name, rest))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			continue
		}
		if best == "" || compareVersions(name, best) > 0 {
			best = name
		}
	}
	return best
}

func compareVersions(a, b string) int {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) })
	}
	x, y := split(a), split(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		m, errM := strconv.ParseUint(x[i], 10, 64)
		n, errN := strconv.ParseUint(y[i], 10, 64)
		if errM == nil && errN == nil && m != n {
			if m > n {
				return 1
			}
			return -1
		}
	}
	switch {
	case len(x) > len(y):
		return 1
	case len(x) < len(y):
		return -1
	}
	return strings.Compare(a, b)
}

// currentLink is the link text to restore for a recorded symlink entry: the
// recorded one, or, for a version store, the same link moved to the newest
// installed version, as the agent's updater would have switched it.
func currentLink(link, target string) string {
	store, version, rest, ok := versionStore(target)
	if !ok {
		return link
	}
	newest := newestVersion(store, rest)
	if newest == "" || newest == version {
		return link
	}
	old := string(filepath.Separator) + "versions" + string(filepath.Separator) + version
	if strings.Count(link, old) == 1 {
		return strings.Replace(link, old, string(filepath.Separator)+"versions"+string(filepath.Separator)+newest, 1)
	}
	return filepath.Join(store, newest, rest)
}
