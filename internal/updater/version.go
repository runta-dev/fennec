// Package updater keeps Fennec current from signed release feeds, adapted from
// yetone/magpie's updater architecture for Fennec's single-daemon control plane.
package updater

import (
	"cmp"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var gitDescribePre = regexp.MustCompile(`^\d+-g[0-9a-f]+`)
var plistShortVersion = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)

type semver struct {
	n   [3]int
	pre string
}

func parseVersion(raw string) *semver {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	v, _, _ = strings.Cut(v, "+")
	v, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	var parsed semver
	for i, part := range parts {
		num, err := strconv.Atoi(part)
		if err != nil || num < 0 {
			return nil
		}
		parsed.n[i] = num
	}
	parsed.pre = pre
	return &parsed
}

// NormalizeVersion strips a leading "v" and validates a 3-part semantic version.
func NormalizeVersion(raw string) (string, bool) {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if parseVersion(v) == nil {
		return "", false
	}
	return v, true
}

// Released reports whether v is a published release version rather than a
// local development fallback ("0.2.0", "dev", or git-describe suffixes).
func Released(v string) bool {
	s := parseVersion(v)
	if s == nil {
		return false
	}
	if s.n == [3]int{0, 2, 0} && s.pre == "" {
		return false
	}
	return !gitDescribePre.MatchString(s.pre) && !strings.Contains(s.pre, "dirty")
}

// Newer reports whether version a is strictly newer than version b.
func Newer(a, b string) bool {
	x, y := parseVersion(a), parseVersion(b)
	if x == nil || y == nil {
		return false
	}
	for i := range 3 {
		if x.n[i] != y.n[i] {
			return x.n[i] > y.n[i]
		}
	}
	return comparePrerelease(x.pre, y.pre) > 0
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		m, errM := strconv.ParseUint(x[i], 10, 64)
		n, errN := strconv.ParseUint(y[i], 10, 64)
		switch {
		case errM == nil && errN == nil:
			if m != n {
				return cmp.Compare(m, n)
			}
		case errM == nil:
			return -1
		case errN == nil:
			return 1
		default:
			if c := strings.Compare(x[i], y[i]); c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(len(x), len(y))
}

// BundlePath returns the enclosing macOS .app bundle for executable, or "".
func BundlePath(executable string) string {
	if executable == "" {
		return ""
	}
	cleaned := filepath.Clean(executable)
	if filepath.Base(filepath.Dir(cleaned)) != "MacOS" {
		return ""
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(cleaned)))
	if !strings.EqualFold(filepath.Ext(app), ".app") {
		return ""
	}
	return app
}

// BundleVersion reads CFBundleShortVersionString from a macOS .app bundle.
func BundleVersion(bundle string) string {
	if override := strings.TrimSpace(os.Getenv("FENNEC_UPDATE_CURRENT_VERSION")); override != "" {
		return override
	}
	if bundle == "" {
		return "dev"
	}
	raw, err := os.ReadFile(filepath.Join(bundle, "Contents/Info.plist"))
	if err != nil {
		return "dev"
	}
	match := plistShortVersion.FindSubmatch(raw)
	if match == nil {
		return "dev"
	}
	v := strings.TrimSpace(string(match[1]))
	if v == "" {
		return "dev"
	}
	return v
}
