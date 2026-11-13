package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Incremental updates. A release publishes the app as content-addressed
// components besides the full zip: "core" (Fennec.app without the large
// nested bundles) and one archive per nested bundle, such as the signed
// Chromium.app. A component's tree digest names its unpacked content, so an
// update reuses the installed copy (an APFS clone) whenever the digest the new
// release needs equals the installed one, and downloads only what changed.
// The reassembled bundle is byte-identical to the signed and notarized release
// bundle, so the outer seal and the stapled ticket stay valid; it is verified
// with codesign like any downloaded bundle before anything is installed.

const (
	// ComponentsFormat is the version of both the release component manifest
	// and the bundle's Contents/Resources/components.json.
	ComponentsFormat = 1
	// CoreComponent is the component holding everything except the nested
	// bundles listed by the other components.
	CoreComponent = "core"
	// ChromiumComponent is the signed Chromium.app driven by fennec-cua.
	ChromiumComponent = "chromium"
	// ChromiumComponentPath is where ChromiumComponent lives in Fennec.app.
	ChromiumComponentPath = "Contents/Helpers/Chromium.app"
	// BundleComponentsPath is the signed component manifest inside Fennec.app.
	BundleComponentsPath = "Contents/Resources/components.json"
)

// Component describes one part of a released Fennec.app.
type Component struct {
	Name string `json:"name"`
	// Path is the bundle-relative location of a nested component; empty for core.
	Path string `json:"path,omitempty"`
	// Tree is TreeDigest of the unpacked component; empty for core.
	Tree string `json:"tree,omitempty"`
	// Key identifies what produced the component (for Chromium: the signing
	// identity and packager sources). Release tooling uses it to prove that an
	// unchanged input kept an unchanged tree; the updater ignores it.
	Key string `json:"key,omitempty"`
	// Asset, Size and SHA256 describe the downloadable archive (feed only).
	Asset  string `json:"asset,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// ComponentSet is the release component manifest
// (Fennec-<version>-macos-<arch>-components.json) or, without archive fields,
// the bundle's own Contents/Resources/components.json.
type ComponentSet struct {
	Format     int         `json:"format"`
	Version    string      `json:"version,omitempty"`
	Arch       string      `json:"arch,omitempty"`
	Components []Component `json:"components"`
}

// ComponentsAssetName returns the release component manifest name.
func ComponentsAssetName(version, arch string) string {
	return fmt.Sprintf("Fennec-%s-macos-%s-components.json", version, arch)
}

// CoreAssetName returns the core component archive name.
func CoreAssetName(version, arch string) string {
	return fmt.Sprintf("Fennec-%s-macos-%s-core.zip", version, arch)
}

// NestedAssetName returns the content-addressed archive name of a nested
// component: unchanged content keeps its name across releases.
func NestedAssetName(name, tree, arch string) string {
	return fmt.Sprintf("Fennec-%s-%s-macos-%s.zip", name, tree[:16], arch)
}

// Find returns the named component.
func (s *ComponentSet) Find(name string) (Component, bool) {
	if s == nil {
		return Component{}, false
	}
	for _, c := range s.Components {
		if c.Name == name {
			return c, true
		}
	}
	return Component{}, false
}

// Nested returns the components other than core.
func (s *ComponentSet) Nested() []Component {
	if s == nil {
		return nil
	}
	var out []Component
	for _, c := range s.Components {
		if c.Name != CoreComponent {
			out = append(out, c)
		}
	}
	return out
}

func validComponentName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func validNestedPath(p string) bool {
	if p == "" || strings.Contains(p, "\\") || path.Clean(p) != p || path.IsAbs(p) {
		return false
	}
	if !strings.HasPrefix(p, "Contents/") || !strings.HasSuffix(p, ".app") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validDigest(s string) bool { return normalizeSHA256(s) == s && s != "" }

func validAssetName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\") && name != "." && name != ".." && strings.HasSuffix(name, ".zip")
}

// validateBundle checks the bundle form: nested components only, each with a
// path and a tree digest.
func (s *ComponentSet) validateBundle() error {
	if s.Format != ComponentsFormat {
		return fmt.Errorf("unsupported component manifest format %d", s.Format)
	}
	seenName := map[string]bool{}
	var paths []string
	for _, c := range s.Components {
		if !validComponentName(c.Name) || c.Name == CoreComponent || seenName[c.Name] {
			return fmt.Errorf("invalid component name %q", c.Name)
		}
		seenName[c.Name] = true
		if !validNestedPath(c.Path) {
			return fmt.Errorf("component %s has invalid path %q", c.Name, c.Path)
		}
		if !validDigest(c.Tree) {
			return fmt.Errorf("component %s has invalid tree digest", c.Name)
		}
		for _, other := range paths {
			if other == c.Path || strings.HasPrefix(other, c.Path+"/") || strings.HasPrefix(c.Path, other+"/") {
				return fmt.Errorf("component paths %s and %s overlap", other, c.Path)
			}
		}
		paths = append(paths, c.Path)
	}
	return nil
}

// validateRelease checks the feed form: exactly one core component, every
// component with a downloadable archive, nested ones as in validateBundle.
func (s *ComponentSet) validateRelease(version, arch string) error {
	if s.Format != ComponentsFormat {
		return fmt.Errorf("unsupported component manifest format %d", s.Format)
	}
	if s.Version != version || s.Arch != arch {
		return fmt.Errorf("component manifest is for %s/%s, not %s/%s", s.Version, s.Arch, version, arch)
	}
	nested := ComponentSet{Format: s.Format}
	cores := 0
	assets := map[string]bool{}
	for _, c := range s.Components {
		if !validAssetName(c.Asset) || assets[c.Asset] || c.Size <= 0 || !validDigest(c.SHA256) {
			return fmt.Errorf("component %s has an invalid archive", c.Name)
		}
		assets[c.Asset] = true
		if c.Name == CoreComponent {
			if c.Path != "" || c.Tree != "" {
				return fmt.Errorf("core component must not name a path")
			}
			cores++
			continue
		}
		nested.Components = append(nested.Components, c)
	}
	if cores != 1 {
		return fmt.Errorf("component manifest needs exactly one core component")
	}
	return nested.validateBundle()
}

// ParseBundleComponents decodes a bundle's components.json.
func ParseBundleComponents(raw []byte) (*ComponentSet, error) {
	var set ComponentSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("decode component manifest: %w", err)
	}
	if err := set.validateBundle(); err != nil {
		return nil, err
	}
	return &set, nil
}

// ParseReleaseComponents decodes and validates a release component manifest.
func ParseReleaseComponents(raw []byte, version, arch string) (*ComponentSet, error) {
	var set ComponentSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("decode component manifest: %w", err)
	}
	for i := range set.Components {
		set.Components[i].SHA256 = normalizeSHA256(set.Components[i].SHA256)
	}
	if err := set.validateRelease(version, arch); err != nil {
		return nil, err
	}
	return &set, nil
}

// ReadBundleComponents reads app's Contents/Resources/components.json.
func ReadBundleComponents(app string) (*ComponentSet, error) {
	file, err := os.Open(filepath.Join(app, BundleComponentsPath))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return nil, err
	}
	return ParseBundleComponents(raw)
}

// TreeDigest returns a SHA-256 over a directory tree: every entry's relative
// path and type, regular files' content and executable bit, and symlink
// targets, in lexical order. Ownership, timestamps, other permission bits and
// extended attributes are not part of it; the code signature check covers
// what matters of those.
func TreeDigest(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch mode := d.Type(); {
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "l\x00%s\x00%s\n", rel, target)
		case d.IsDir():
			fmt.Fprintf(h, "d\x00%s\n", rel)
		case mode.IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			file, err := os.Open(p)
			if err != nil {
				return err
			}
			fh := sha256.New()
			_, copyErr := io.CopyBuffer(fh, file, buf)
			file.Close()
			if copyErr != nil {
				return copyErr
			}
			exec := 0
			if fi.Mode().Perm()&0o111 != 0 {
				exec = 1
			}
			fmt.Fprintf(h, "f\x00%s\x00%d\x00%x\n", rel, exec, fh.Sum(nil))
		default:
			return fmt.Errorf("unsupported file type at %s", rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FetchComponents downloads and validates the release component manifest for
// arch. It returns nil without error when the release publishes none (older
// releases), so callers fall back to the full archive.
func FetchComponents(ctx context.Context, httpClient *http.Client, rel *Release, arch string) (*ComponentSet, error) {
	if rel == nil {
		return nil, nil
	}
	asset, ok := rel.Assets[ComponentsAssetName(rel.Version, arch)]
	if !ok {
		return nil, nil
	}
	if asset.URL == "" || normalizeSHA256(asset.SHA256) == "" {
		return nil, fmt.Errorf("component manifest has no verified digest")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Fennec-Updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("component manifest HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return nil, fmt.Errorf("invalid component manifest response")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != normalizeSHA256(asset.SHA256) {
		return nil, fmt.Errorf("component manifest failed SHA-256 verification")
	}
	set, err := ParseReleaseComponents(raw, rel.Version, arch)
	if err != nil {
		return nil, err
	}
	for _, c := range set.Components {
		published, ok := rel.Assets[c.Asset]
		if c.Name == CoreComponent && (!ok || published.URL == "") {
			return nil, fmt.Errorf("release has no %s asset", c.Asset)
		}
		if ok && published.SHA256 != "" && published.SHA256 != c.SHA256 {
			return nil, fmt.Errorf("component %s digest disagrees with the release", c.Name)
		}
	}
	return set, nil
}

// UpdatePlan is what staging a release is expected to download and reuse.
type UpdatePlan struct {
	// Components is false when the full archive will be downloaded.
	Components bool
	Download   int64
	Reused     []string
	Saved      int64
}

// PlanUpdate predicts the bytes an update of bundle to rel downloads, from the
// release component manifest and the installed bundle's components.json. When
// nothing can be reused the full archive is cheaper and simpler.
func PlanUpdate(rel *Release, bundle, arch string) UpdatePlan {
	full := UpdatePlan{}
	if rel == nil {
		return full
	}
	if asset, ok := rel.Assets[AppAssetName(rel.Version, arch)]; ok {
		full.Download = asset.Size
	}
	set := rel.Components
	core, ok := set.Find(CoreComponent)
	if !ok || bundle == "" {
		return full
	}
	installed, _ := ReadBundleComponents(bundle)
	plan := UpdatePlan{Components: true, Download: core.Size}
	for _, c := range set.Nested() {
		have, ok := installed.Find(c.Name)
		if ok && have.Path == c.Path && have.Tree == c.Tree && dirExists(filepath.Join(bundle, filepath.FromSlash(c.Path))) {
			plan.Reused = append(plan.Reused, c.Name)
			plan.Saved += c.Size
			continue
		}
		if _, published := rel.Assets[c.Asset]; !published {
			return full
		}
		plan.Download += c.Size
	}
	if len(plan.Reused) == 0 {
		return full
	}
	return plan
}

func dirExists(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}
