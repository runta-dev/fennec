// Package credits generates Chromium's third-party notices (the
// about:credits page and a plain-text bundle) for a pinned Chromium
// commit, in Go. It reimplements the rules of Chromium's
// tools/licenses/licenses.py "credits" command as an official macOS
// build runs it (--target-os=mac, no gn target: every third-party
// directory of the checkout is scanned and the ones whose metadata says
// they ship are credited) over a source tree: an on-disk checkout, or the
// pinned commit and its DEPS read from the Chromium git servers
// (FetchTree). No Python is run.
//
// The rule tables below are those of licenses.py at the pinned commits
// (Copyright The Chromium Authors, BSD-3-Clause); RulesBlob names that
// file's git blob so a pin bump that changes it is noticed.
package credits

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// RulesBlob is the git blob id of tools/licenses/licenses.py whose rules
// this package implements.
const RulesBlob = "79b6b85671304c5f64f3a92b5ac36dc2e1e7a6ec"

// RulesPath is where licenses.py lives in the source tree.
const RulesPath = "tools/licenses/licenses.py"

var metadataFileNames = []string{"README.chromium", "README.crashpad", "README.v8", "README.pdfium", "README.angle"}

const (
	fileTemplate       = "components/webui/about/resources/about_credits.tmpl"
	entryTemplate      = "components/webui/about/resources/about_credits_entry.tmpl"
	reciprocalTemplate = "components/webui/about/resources/about_credits_reciprocal.tmpl"
)

var chromiumMetadata = Metadata{
	Name: "The Chromium Project", URL: "https://www.chromium.org", License: "BSD-3-Clause",
	Shipped: "yes", LicenseFiles: []string{"LICENSE"}, Dir: "",
}

var prunePaths = setOf(
	"third_party/adobe", "third_party/android_tools_internal", "third_party/crubit",
	"third_party/widevine", "third_party/valgrind", "components/variations/test_data/cipd",
	"build/secondary", "third_party/bison", "third_party/chromite", "third_party/clang-format",
	"third_party/cygwin", "third_party/gnu_binutils", "third_party/gold", "third_party/gperf",
	"third_party/llvm", "third_party/llvm-bootstrap", "third_party/llvm-bootstrap-install",
	"third_party/llvm-build", "third_party/llvm-build-tools", "third_party/mingw-w64",
	"third_party/nacl_sdk_binaries", "third_party/pefile", "third_party/perl",
	"third_party/psyco_win32", "third_party/pyelftools", "third_party/pylib",
	"third_party/pywebsocket", "third_party/rust-src", "third_party/rust-toolchain-intermediate",
	"third_party/syzygy", "third_party/amd", "third_party/clear_cache", "third_party/gnu",
	"third_party/googlemac", "third_party/pcre", "third_party/psutils", "third_party/sawbuck",
	"third_party/wix", "chrome/browser/resources/chromeos/quickoffice", "third_party/barhopper",
	"components/optimization_guide/internal/third_party", "third_party/devtools-frontend-internal",
	"third_party/directxsdk", "isolate_deps_dir", "tools/binary_size/libsupersize/testdata",
	"tools/catapult_bisect_dep", "third_party/webrtc_overrides", "third_party/pruned",
	"buildtools/third_party/libc++", "buildtools/third_party/libc++abi", "buildtools/third_party/libunwind",
)

var pruneDirs = setOf(".svn", ".git", "layout_tests")

var vcsDirs = setOf(".svn", ".git")

const additionalPathsFile = "additional_readme_paths.json"

var additionalPaths = []string{
	"chrome/test/chromeos/autotest", "chrome/test/data", "third_party/boringssl/src/third_party/fiat",
	"third_party/devtools-frontend/src/front_end/third_party",
	"third_party/devtools-frontend-internal/front_end/third_party", "tools/page_cycler/acid3",
	"url/third_party/mozilla", "v8", "v8/strongtalk", "v8/fdlibm",
}

// specialCases are directories whose metadata licenses.py holds itself.
var specialCases = map[string]map[string]any{
	"third_party/cros_system_api": {"Name": "Chromium OS system API", "URL": "https://www.chromium.org/chromium-os",
		"Shipped": "yes", "License": "BSD-3-Clause", "License File": []string{"//LICENSE.chromium_os"}},
	"third_party/ipcz": {"Name": "ipcz", "URL": "https://chromium.googlesource.com/chromium/src/third_party/ipcz",
		"Shipped": "yes", "License": "BSD-3-Clause", "License File": []string{"//third_party/ipcz/LICENSE"}},
	"third_party/lss": {"Name": "linux-syscall-support", "URL": "https://chromium.googlesource.com/linux-syscall-support/",
		"Shipped": "yes", "License": "BSD-3-Clause", "License File": []string{"//third_party/lss/LICENSE"}},
	"third_party/openscreen/src/third_party/abseil": {"Name": "abseil", "URL": "https://github.com/abseil/abseil-cpp/",
		"Shipped": "yes", "License": "Apache-2.0", "License File": []string{"//third_party/abseil-cpp/LICENSE"}},
	"third_party/openscreen/src/third_party/boringssl": {"Name": "BoringSSL", "URL": "https://boringssl.googlesource.com/boringssl/",
		"Shipped": "yes", "License": "Apache-2.0, BSD-3-Clause", "License File": []string{"//third_party/boringssl/src/LICENSE"}},
	"third_party/openscreen/src/third_party/jsoncpp": {"Name": "jsoncpp", "URL": "https://github.com/open-source-parsers/jsoncpp",
		"Shipped": "yes", "License": "MIT", "License File": []string{"//third_party/jsoncpp/src/LICENSE"}},
	"third_party/pdfium": {"Name": "PDFium", "URL": "https://pdfium.googlesource.com/pdfium/",
		"Shipped": "yes", "License": "Apache-2.0, BSD-3-Clause"},
	"third_party/blink": {"Name": "WebKit", "URL": "https://webkit.org/", "Shipped": "yes",
		"License": "BSD-2-Clause, BSD-3-Clause, LGPL-2.0, LGPL-2.1", "License File": []string{"//third_party/blink/LICENSE_FOR_ABOUT_CREDITS"}},
	"v8": {"Name": "V8 JavaScript Engine", "URL": "https://v8.dev/", "Shipped": "yes", "License": "BSD-3-Clause"},
	"v8/strongtalk": {"Name": "Strongtalk", "URL": "https://www.strongtalk.org/", "Shipped": "yes",
		"License": "BSD-3-Clause", "License File": []string{"//v8/LICENSE.strongtalk"}},
	"v8/fdlibm": {"Name": "fdlibm", "URL": "https://www.netlib.org/fdlibm/", "Shipped": "yes", "License": "SunPro",
		"License File": []string{"//v8/LICENSE.fdlibm"}, "License Android Compatible": "yes"},
	"third_party/swiftshader": {"Name": "SwiftShader", "URL": "https://swiftshader.googlesource.com/SwiftShader",
		"Shipped": "yes", "License": "Apache-2.0", "License Android Compatible": "yes", "License File": []string{"//third_party/swiftshader/LICENSE.txt"}},
	"third_party/swiftshader/third_party/SPIRV-Tools": {"Name": "SPIRV-Tools", "URL": "https://github.com/KhronosGroup/SPIRV-Tools",
		"Shipped": "yes", "License": "Apache-2.0", "License File": []string{"//third_party/swiftshader/third_party/SPIRV-Tools/LICENSE"}},
	"third_party/swiftshader/third_party/SPIRV-Headers": {"Name": "SPIRV-Headers", "URL": "https://github.com/KhronosGroup/SPIRV-Headers",
		"Shipped": "yes", "License": "Khronos, MIT, CC-BY-4.0", "License File": []string{"//third_party/swiftshader/third_party/SPIRV-Headers/LICENSE"}},
	"third_party/sample3": {"Name": "Sample 3", "URL": "https://sample3", "Shipped": "yes", "License": "Apache-2.0",
		"License File": []string{"//third_party/sample3/the_license"}},
}

var (
	mandatoryFields   = []string{"Name", "URL", "License", "License File", "Shipped"}
	aliasFields       = map[string]string{"Shipped in Chromium": "Shipped"}
	optionalFields    = []string{"License Android Compatible"}
	dependencyDivider = regexp.MustCompile(`^-{20} DEPENDENCY DIVIDER -{20}$`)
)

const notShipped = "NOT_SHIPPED"

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, x := range s {
		m[x] = true
	}
	return m
}

// Metadata is one credited dependency.
type Metadata struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	License      string   `json:"license"`
	Shipped      string   `json:"shipped"`
	LicenseFiles []string `json:"licenseFiles"` // resolved, relative to the root
	Dir          string   `json:"dir"`
	Source       string   `json:"source"` // the metadata file, or "special case"
}

// Problem is a dependency that licenses.py would report and leave out.
type Problem struct {
	Dir     string `json:"dir"`
	Message string `json:"message"`
}

// Scan is the result of FindThirdPartyDirs and ParseDir over a tree.
type Scan struct {
	Dirs     []string   // the third-party directories found
	Credited []Metadata // shipped dependencies, sorted, Chromium first
	Skipped  []Problem  // directories or metadata left out, with why
}

// prefetch asks a Prefetcher tree for files; others read on demand.
func prefetch(t Tree, paths []string) error {
	if p, ok := t.(Prefetcher); ok && len(paths) > 0 {
		return p.Prefetch(paths)
	}
	return nil
}

// containsFiles is _ContainsFiles: a file anywhere below the directory
// (VCS directories skipped). Links to directories are listed but not
// entered, as os.walk does.
func containsFiles(t Tree, dir string, memo map[string]bool) bool {
	rp, k, err := resolve(t, dir)
	if err != nil || k != KindDir {
		return false
	}
	return containsFilesPhys(t, rp, memo)
}

func containsFilesPhys(t Tree, dir string, memo map[string]bool) bool {
	if v, ok := memo[dir]; ok {
		return v
	}
	es, _ := t.ReadDir(dir)
	found := false
	var sub []string
	for _, e := range es {
		switch e.Kind {
		case KindFile:
			found = true
		case KindSymlink:
			p := path.Join(dir, e.Name)
			if _, k, _ := resolve(t, p); k != KindDir {
				found = true // a link to a file, or a dangling one
			}
		case KindDir:
			if !vcsDirs[e.Name] {
				sub = append(sub, path.Join(dir, e.Name))
			}
		}
		if found {
			break
		}
	}
	if !found {
		for _, s := range sub {
			if containsFilesPhys(t, s, memo) {
				found = true
				break
			}
		}
	}
	memo[dir] = found
	return found
}

// isDirEntry is os.walk's split: an entry is listed among directories
// when it is one or links to one.
func isDirEntry(t Tree, dir string, e DirEntry) bool {
	switch e.Kind {
	case KindDir:
		return true
	case KindSymlink:
		_, k, _ := resolve(t, path.Join(dir, e.Name))
		return k == KindDir
	}
	return false
}

// FindThirdPartyDirs walks the tree as licenses.py's FindThirdPartyDirs
// does and answers the sorted third-party directories.
func FindThirdPartyDirs(t Tree) ([]string, error) {
	// additional_readme_paths.json files steer the walk: read them first.
	var jsons []string
	err := walkTree(t, func(dir string, files []string) {
		for _, f := range files {
			if f == additionalPathsFile {
				jsons = append(jsons, path.Join(dir, f))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if err := prefetch(t, jsons); err != nil {
		return nil, err
	}
	found := map[string]bool{}
	memo := map[string]bool{}
	var maybeAdd func(dirpath string) error
	maybeAdd = func(dirpath string) error {
		if prunePaths[dirpath] || found[dirpath] {
			return nil
		}
		if _, k, _ := resolve(t, dirpath); k == KindNone || !containsFiles(t, dirpath, memo) {
			return nil
		}
		found[dirpath] = true
		jp := path.Join(dirpath, additionalPathsFile)
		if _, k, _ := resolve(t, jp); k == KindNone {
			return nil
		}
		text, err := readText(t, jp)
		if err != nil {
			return err
		}
		var extra []string
		if err := json.Unmarshal([]byte(text), &extra); err != nil {
			return fmt.Errorf("%s: %w", jp, err)
		}
		for _, p := range extra {
			if path.IsAbs(p) {
				continue
			}
			sub := path.Clean(path.Join(dirpath, p))
			if sub == ".." || strings.HasPrefix(sub, "../") {
				continue
			}
			if err := maybeAdd(sub); err != nil {
				return err
			}
		}
		return nil
	}
	additional := setOf(additionalPaths...)
	var walkErr error
	var walk func(dir string)
	walk = func(dir string) {
		if walkErr != nil {
			return
		}
		if prunePaths[dir] || strings.HasPrefix(dir, "out") {
			return
		}
		es, err := t.ReadDir(dir)
		if err != nil {
			walkErr = err
			return
		}
		var dirs []string
		if !additional[dir] {
			for _, e := range es {
				if isDirEntry(t, dir, e) && !pruneDirs[e.Name] {
					dirs = append(dirs, e.Name)
				}
			}
		}
		if path.Base(dir) == "third_party" {
			for _, d := range dirs {
				if err := maybeAdd(path.Join(dir, d)); err != nil {
					walkErr = err
					return
				}
			}
		}
		if parts := splitPath(dir); len(parts) == 4 && parts[0] == "third_party" && parts[1] == "rust" && parts[2] != "chromium_crates_io" {
			if err := maybeAdd(dir); err != nil {
				walkErr = err
				return
			}
		}
		for _, d := range dirs {
			p := path.Join(dir, d)
			if t.Lstat(p) == KindDir { // os.walk does not follow links
				walk(p)
			}
		}
	}
	walk("")
	if walkErr != nil {
		return nil, walkErr
	}
	for _, p := range additionalPaths {
		if err := maybeAdd(p); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(found))
	for d := range found {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// walkTree visits every real directory (not through links), skipping
// pruned paths, with the names of its non-directory entries.
func walkTree(t Tree, fn func(dir string, files []string)) error {
	var walk func(dir string) error
	walk = func(dir string) error {
		if prunePaths[dir] || strings.HasPrefix(dir, "out") {
			return nil
		}
		es, err := t.ReadDir(dir)
		if err != nil {
			return err
		}
		var files []string
		for _, e := range es {
			if e.Kind == KindDir {
				if pruneDirs[e.Name] {
					continue
				}
				if err := walk(path.Join(dir, e.Name)); err != nil {
					return err
				}
			} else {
				files = append(files, e.Name)
			}
		}
		fn(dir, files)
		return nil
	}
	return walk("")
}

// pySplitlines splits as Python's str.splitlines.
func pySplitlines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			if r == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// pyStrip strips as Python's str.strip.
func pyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x1f
	})
}

// parseMetadataFile is ParseMetadataFile: the dependencies a metadata
// file describes, as field maps.
func parseMetadataFile(text, name string) ([]map[string]any, error) {
	lookup := map[string]string{}
	for _, f := range mandatoryFields {
		lookup[strings.ToLower(f)] = f
	}
	for a := range aliasFields {
		lookup[strings.ToLower(a)] = a
	}
	for _, f := range optionalFields {
		lookup[strings.ToLower(f)] = f
	}
	var deps []map[string]any
	cur := map[string]any{}
	for _, line := range pySplitlines(text) {
		line = pyStrip(line)
		if line == "" {
			continue
		}
		if dependencyDivider.MatchString(line) {
			if len(cur) > 0 {
				deps = append(deps, cur)
			}
			cur = map[string]any{}
			continue
		}
		raw, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		field, ok := lookup[strings.ToLower(raw)]
		if !ok {
			continue
		}
		if _, dup := cur[field]; dup {
			return nil, fmt.Errorf("duplicate '%s' in %s", field, name)
		}
		if field == "License File" {
			var vs []string
			for _, v := range strings.Split(value, ",") {
				vs = append(vs, pyStrip(v))
			}
			cur[field] = vs
		} else {
			cur[field] = value
		}
	}
	if len(cur) > 0 {
		deps = append(deps, cur)
	}
	return deps, nil
}

// resolveLicensePath is _ResolvePath: a License File value relative to
// the dependency (or to the root when it starts with "/"), answered only
// when it exists inside the tree.
func resolveLicensePath(t Tree, dir, file string) (string, bool) {
	var p string
	if strings.HasPrefix(file, "/") {
		p = path.Join(strings.TrimLeft(file, "/"))
	} else {
		p = path.Join(dir, file)
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	if p == "." {
		p = ""
	}
	if _, k, _ := resolve(t, p); k == KindNone {
		return "", false
	}
	return p, true
}

// processLicenseFiles is process_license_files.
func processLicenseFiles(t Tree, dir string, files []string) []string {
	var out []string
	for _, f := range files {
		if f == notShipped {
			continue
		}
		if p, ok := resolveLicensePath(t, dir, f); ok {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		if p, ok := resolveLicensePath(t, dir, "COPYING"); ok {
			out = append(out, p)
		}
	}
	return out
}

// processMetadata is ProcessMetadata: it fills defaults, resolves license
// files and answers what is wrong.
func processMetadata(t Tree, m map[string]any, dir string) []string {
	var errs []string
	for alias, field := range aliasFields {
		if v, ok := m[alias]; ok {
			m[field] = v
			delete(m, alias)
		}
	}
	if _, ok := m["License File"]; !ok {
		m["License File"] = []string{"LICENSE"}
	}
	files, _ := m["License File"].([]string)
	if s, _ := m["Shipped"].(string); s == "" {
		shipped := "yes"
		for _, f := range files {
			if f == notShipped {
				shipped = "no"
			}
		}
		m["Shipped"] = shipped
	}
	for _, f := range []string{"Name", "URL", "License", "Shipped"} {
		if s, _ := m[f].(string); s == "" {
			errs = append(errs, fmt.Sprintf("couldn't find '%s' line in README.chromium or licenses.py SPECIAL_CASES", f))
		}
	}
	if len(files) == 0 {
		errs = append(errs, "couldn't find 'License File' line in README.chromium or licenses.py SPECIAL_CASES")
	}
	resolved := processLicenseFiles(t, dir, files)
	if m["Shipped"] == "yes" && len(resolved) == 0 {
		errs = append(errs, "license file not found")
	}
	m["License File"] = resolved
	return errs
}

func toMetadata(m map[string]any, dir, source string) Metadata {
	s := func(k string) string { v, _ := m[k].(string); return v }
	files, _ := m["License File"].([]string)
	return Metadata{Name: s("Name"), URL: s("URL"), License: s("License"), Shipped: s("Shipped"),
		LicenseFiles: append([]string{}, files...), Dir: dir, Source: source}
}

// parseDir is ParseDir for one third-party directory.
func parseDir(t Tree, dir string) ([]Metadata, []Problem) {
	if sc, ok := specialCases[dir]; ok {
		m := map[string]any{}
		for k, v := range sc {
			if l, ok := v.([]string); ok {
				v = append([]string{}, l...)
			}
			m[k] = v
		}
		var probs []Problem
		for _, e := range processMetadata(t, m, dir) {
			probs = append(probs, Problem{dir, "special case: " + e})
		}
		// licenses.py keeps a special case even when it reports errors.
		return []Metadata{toMetadata(m, dir, "special case")}, probs
	}
	var out []Metadata
	var probs []Problem
	any := false
	for _, name := range metadataFileNames {
		p := path.Join(dir, name)
		if _, k, _ := resolve(t, p); k == KindNone {
			continue
		}
		any = true
		text, err := readText(t, p)
		if err != nil {
			probs = append(probs, Problem{dir, err.Error()})
			continue
		}
		deps, err := parseMetadataFile(text, p)
		if err != nil {
			probs = append(probs, Problem{dir, "invalid metadata file: " + err.Error()})
			continue
		}
		for _, m := range deps {
			if errs := processMetadata(t, m, dir); len(errs) > 0 {
				probs = append(probs, Problem{dir, p + ": " + strings.Join(errs, "; ")})
				continue
			}
			out = append(out, toMetadata(m, dir, p))
		}
	}
	if !any {
		probs = append(probs, Problem{dir, "missing third party metadata file or licenses.py SPECIAL_CASES entry"})
	}
	return out, probs
}

// ScanTree finds and parses every third-party directory, keeping the
// shipped ones as licenses.py credits does.
func ScanTree(t Tree) (*Scan, error) {
	if err := checkRules(t); err != nil {
		return nil, err
	}
	dirs, err := FindThirdPartyDirs(t)
	if err != nil {
		return nil, err
	}
	var readmes []string
	for _, d := range dirs {
		for _, n := range metadataFileNames {
			p := path.Join(d, n)
			if t.Lstat(p) != KindNone {
				readmes = append(readmes, p)
			}
		}
	}
	if err := prefetch(t, readmes); err != nil {
		return nil, err
	}
	s := &Scan{Dirs: dirs}
	for _, d := range dirs {
		ms, probs := parseDir(t, d)
		s.Skipped = append(s.Skipped, probs...)
		for _, m := range ms {
			if m.Shipped == "no" {
				continue
			}
			s.Credited = append(s.Credited, m)
		}
	}
	sort.SliceStable(s.Credited, func(i, j int) bool {
		a, b := s.Credited[i], s.Credited[j]
		if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
			return la < lb
		}
		return a.Dir < b.Dir
	})
	c := chromiumMetadata
	c.LicenseFiles = append([]string{}, c.LicenseFiles...)
	c.Source = "licenses.py"
	s.Credited = append([]Metadata{c}, s.Credited...)
	return s, nil
}

// checkRules refuses a tree whose licenses.py is not the one these rules
// come from (when the tree has one; test fixtures need not).
func checkRules(t Tree) error {
	if t.Lstat(RulesPath) == KindNone {
		return nil
	}
	if err := prefetch(t, []string{RulesPath}); err != nil {
		return err
	}
	data, err := t.ReadFile(RulesPath)
	if err != nil {
		return err
	}
	if id := gitBlobID(data); id != RulesBlob {
		return fmt.Errorf("%s at this commit is blob %s; the Go rules implement %s: review the changes and update internal/credits", RulesPath, id, RulesBlob)
	}
	return nil
}

// gitBlobID is the git object id of a file's content.
func gitBlobID(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// Output is the generated notices.
type Output struct {
	HTML       []byte // the about:credits page, byte for byte as licenses.py writes it
	Text       []byte // licenses.py license_file --format txt
	Components int    // entries on the page (after duplicates are merged)
	Scan       *Scan
}

// SHA256 is the hex digest of the HTML.
func (o *Output) SHA256() string {
	h := sha256.Sum256(o.HTML)
	return hex.EncodeToString(h[:])
}

// pyEscape is Python's html.escape(s, quote=True).
func pyEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace(s)
}

// evaluateTemplate replaces {{key}} in order, as licenses.py does.
func evaluateTemplate(tmpl string, keys []string, env map[string]string, escape bool) string {
	for _, k := range keys {
		v := env[k]
		if escape {
			v = pyEscape(v)
		}
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", v)
	}
	return tmpl
}

// Generate renders the credits of a tree.
func Generate(t Tree) (*Output, error) {
	s, err := ScanTree(t)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, m := range s.Credited {
		files = append(files, m.LicenseFiles...)
	}
	files = append(files, fileTemplate, entryTemplate, reciprocalTemplate)
	if err := prefetch(t, files); err != nil {
		return nil, err
	}
	read := func(p string) (string, error) {
		text, err := readText(t, p)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		return text, nil
	}
	ft, err := read(fileTemplate)
	if err != nil {
		return nil, err
	}
	et, err := read(entryTemplate)
	if err != nil {
		return nil, err
	}
	rt, err := read(reciprocalTemplate)
	if err != nil {
		return nil, err
	}
	type entry struct{ name, content string }
	var entries []*entry
	byName := map[string]*entry{}
	var text []string
	for _, m := range s.Credited {
		var parts []string
		for _, f := range m.LicenseFiles {
			c, err := read(f)
			if err != nil {
				return nil, err
			}
			parts = append(parts, c)
		}
		if len(m.LicenseFiles) > 0 {
			text = append(text, strings.Repeat("-", 20), m.Name, strings.Repeat("-", 20))
			text = append(text, parts...)
		}
		e := &entry{name: m.Name, content: evaluateTemplate(et, []string{"name", "url", "license"},
			map[string]string{"name": m.Name, "url": m.URL, "license": strings.Join(parts, "\n\n")}, true)}
		key := strings.ToLower(e.name)
		prev, seen := byName[key]
		if !seen {
			byName[key] = e
		} else if strings.ToLower(prev.content) == strings.ToLower(e.content) {
			continue
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if la, lb := strings.ToLower(a.name), strings.ToLower(b.name); la != lb {
			return la < lb
		}
		return a.content < b.content
	})
	contents := make([]string, len(entries))
	for i, e := range entries {
		contents[i] = strings.ReplaceAll(e.content, "{{id}}", strconv.Itoa(i))
	}
	recip := evaluateTemplate(rt, []string{"opensource_project", "opensource_link"},
		map[string]string{"opensource_project": "Chromium", "opensource_link": "https://source.chromium.org/chromium"}, false)
	page := "<!-- Generated by licenses.py; do not edit. -->" + evaluateTemplate(ft, []string{"entries", "reciprocal-license-statement"},
		map[string]string{"entries": strings.Join(contents, "\n"), "reciprocal-license-statement": recip}, false)
	return &Output{HTML: []byte(page), Text: []byte(strings.Join(text, "\n")), Components: len(entries), Scan: s}, nil
}
