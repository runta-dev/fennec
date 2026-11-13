package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type fixtureFile struct {
	data string
	exec bool
}

// writeTree materializes files under dir.
func writeTree(t *testing.T, dir string, files map[string]fixtureFile) {
	t.Helper()
	for name, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if f.exec {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(f.data), mode); err != nil {
			t.Fatal(err)
		}
	}
}

// zipTree returns a zip with files under prefix/, like ditto --keepParent.
func zipTree(t *testing.T, prefix string, files map[string]fixtureFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := files[name]
		hdr := &zip.FileHeader{Name: prefix + "/" + name, Method: zip.Deflate}
		if f.exec {
			hdr.SetMode(0o755)
		} else {
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func plistFor(version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runta.fennec</string><key>CFBundleShortVersionString</key><string>` + version + `</string></dict></plist>`
}

func chromiumFiles(build string) map[string]fixtureFile {
	return map[string]fixtureFile{
		"Contents/Info.plist":                   {data: "<plist>chromium " + build + "</plist>"},
		"Contents/MacOS/Chromium":               {data: "chromium-binary-" + build, exec: true},
		"Contents/Frameworks/Framework.dylib":   {data: build + strings.Repeat("f", 96<<10), exec: true},
		"Contents/_CodeSignature/CodeResources": {data: "seal " + build},
	}
}

func treeOf(t *testing.T, files map[string]fixtureFile) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tree")
	writeTree(t, dir, files)
	digest, err := TreeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func sealedManifest(t *testing.T, tree string) string {
	t.Helper()
	raw, err := json.Marshal(ComponentSet{Format: ComponentsFormat, Components: []Component{{Name: ChromiumComponent, Path: ChromiumComponentPath, Tree: tree, Key: "k"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// coreFiles is Fennec.app without Chromium, carrying its sealed manifest.
func coreFiles(t *testing.T, version, chromiumTree string) map[string]fixtureFile {
	return map[string]fixtureFile{
		"Contents/Info.plist":                {data: plistFor(version)},
		"Contents/MacOS/fen":                 {data: "#!/bin/sh\necho " + version + "\n#" + strings.Repeat("x", 48<<10), exec: true},
		"Contents/Resources/components.json": {data: sealedManifest(t, chromiumTree)},
	}
}

func withPrefix(prefix string, files map[string]fixtureFile) map[string]fixtureFile {
	out := map[string]fixtureFile{}
	for name, f := range files {
		out[prefix+"/"+name] = f
	}
	return out
}

func mergeFiles(sets ...map[string]fixtureFile) map[string]fixtureFile {
	out := map[string]fixtureFile{}
	for _, set := range sets {
		for k, v := range set {
			out[k] = v
		}
	}
	return out
}

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// componentRelease serves a fake release feed with a full zip, a core zip, a
// Chromium component zip and the component manifest.
type componentRelease struct {
	t        *testing.T
	version  string
	srv      *httptest.Server
	mu       sync.Mutex
	files    map[string][]byte
	digests  map[string]string // digest the feed advertises, when it differs from the file
	hits     map[string]*atomic.Int32
	omit     map[string]bool
	manifest ComponentSet
	appFiles map[string]fixtureFile // the reference Fennec.app content
}

func newComponentRelease(t *testing.T, version, chromiumBuild string) *componentRelease {
	t.Helper()
	chromium := chromiumFiles(chromiumBuild)
	tree := treeOf(t, chromium)
	core := coreFiles(t, version, tree)
	r := &componentRelease{
		t:        t,
		version:  version,
		files:    map[string][]byte{},
		digests:  map[string]string{},
		hits:     map[string]*atomic.Int32{},
		omit:     map[string]bool{},
		appFiles: mergeFiles(core, withPrefix(ChromiumComponentPath, chromium)),
	}
	arch := "arm64"
	r.files[AppAssetName(version, arch)] = zipTree(t, "Fennec.app", r.appFiles)
	coreZip := zipTree(t, "Fennec.app", core)
	chromiumZip := zipTree(t, "Chromium.app", chromium)
	coreName := CoreAssetName(version, arch)
	chromiumName := NestedAssetName(ChromiumComponent, tree, arch)
	r.files[coreName] = coreZip
	r.files[chromiumName] = chromiumZip
	r.manifest = ComponentSet{Format: ComponentsFormat, Version: version, Arch: arch, Components: []Component{
		{Name: CoreComponent, Asset: coreName, Size: int64(len(coreZip)), SHA256: sum256(coreZip)},
		{Name: ChromiumComponent, Path: ChromiumComponentPath, Tree: tree, Key: "k", Asset: chromiumName, Size: int64(len(chromiumZip)), SHA256: sum256(chromiumZip)},
	}}
	r.publishManifest()
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *componentRelease) publishManifest() {
	raw, err := json.Marshal(r.manifest)
	if err != nil {
		r.t.Fatal(err)
	}
	r.files[ComponentsAssetName(r.version, "arm64")] = raw
}

func (r *componentRelease) hit(name string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.hits[name]; c != nil {
		return c.Load()
	}
	return 0
}

func (r *componentRelease) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path == "/latest" {
		assets := map[string]any{}
		for name, data := range r.files {
			if r.omit[name] {
				continue
			}
			digest := sum256(data)
			if d, ok := r.digests[name]; ok {
				digest = d
			}
			assets[name] = map[string]any{"url": r.srv.URL + "/dl/" + name, "size": len(data), "sha256": digest}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": r.version, "url": r.srv.URL + "/release", "assets": assets})
		return
	}
	name := strings.TrimPrefix(req.URL.Path, "/dl/")
	data, ok := r.files[name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.hits[name] == nil {
		r.hits[name] = &atomic.Int32{}
	}
	r.hits[name].Add(1)
	_, _ = w.Write(data)
}

// installedFennec creates an installed bundle carrying the given Chromium
// build and a components.json describing it (unless manifest is false).
func installedFennec(t *testing.T, chromiumBuild string, manifest bool) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// Outside os.TempDir: RequiresInstallation treats it as a staging path.
	sandbox, err := os.MkdirTemp(home, ".fennec-components-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sandbox) })
	app := filepath.Join(sandbox, "Fennec.app")
	chromium := chromiumFiles(chromiumBuild)
	files := mergeFiles(coreFiles(t, "0.2.100", treeOf(t, chromium)), withPrefix(ChromiumComponentPath, chromium))
	if !manifest {
		delete(files, "Contents/Resources/components.json")
	}
	writeTree(t, app, files)
	return app
}

type progressLog struct {
	mu      sync.Mutex
	maxDone int64
	total   int64
	steps   []string
}

func (p *progressLog) opts() progressOptions {
	return progressOptions{
		onProgress: func(done, total int64) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if done > p.maxDone {
				p.maxDone = done
			}
			p.total = total
		},
		onStep: func(step string) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.steps = append(p.steps, step)
		},
	}
}

func (r *componentRelease) fetch(t *testing.T) *Release {
	t.Helper()
	rel, err := FetchLatest(context.Background(), r.srv.Client(), r.srv.URL+"/latest")
	if err != nil {
		t.Fatal(err)
	}
	set, err := FetchComponents(context.Background(), r.srv.Client(), rel, "arm64")
	if err != nil {
		t.Fatalf("fetch components: %v", err)
	}
	rel.Components = set
	return rel
}

func (r *componentRelease) referenceDigest(t *testing.T) string {
	return treeOf(t, r.appFiles)
}

func acceptSigner(context.Context, string, string) error { return nil }

func stageForTest(t *testing.T, r *componentRelease, rel *Release, installed string, progress *progressLog, verify func(context.Context, string, string) error) (string, []string, UpdatePlan, error) {
	t.Helper()
	plan := PlanUpdate(rel, installed, "arm64")
	staged, reused, err := stageReleaseBundle(context.Background(), r.srv.Client().Transport, rel, installed, "arm64", plan, progress.opts(), verify)
	return staged, reused, plan, err
}

func TestTreeDigestCoversContentModeAndLinks(t *testing.T) {
	base := map[string]fixtureFile{"a/b.txt": {data: "one"}, "bin/tool": {data: "x", exec: true}}
	ref := treeOf(t, base)
	if ref != treeOf(t, base) {
		t.Fatal("digest is not deterministic")
	}
	variants := []map[string]fixtureFile{
		{"a/b.txt": {data: "two"}, "bin/tool": {data: "x", exec: true}},
		{"a/b.txt": {data: "one"}, "bin/tool": {data: "x"}},
		{"a/c.txt": {data: "one"}, "bin/tool": {data: "x", exec: true}},
		{"a/b.txt": {data: "one"}, "bin/tool": {data: "x", exec: true}, "extra": {data: ""}},
	}
	for i, v := range variants {
		if treeOf(t, v) == ref {
			t.Fatalf("variant %d has the reference digest", i)
		}
	}
	dir := filepath.Join(t.TempDir(), "links")
	writeTree(t, dir, base)
	if err := os.Symlink("a/b.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	withLink, err := TreeDigest(dir)
	if err != nil || withLink == ref {
		t.Fatalf("symlink not covered: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "link"))
	if err := os.Symlink("bin/tool", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if other, _ := TreeDigest(dir); other == withLink {
		t.Fatal("symlink target not covered")
	}
	if _, err := TreeDigest(filepath.Join(dir, "a/b.txt")); err == nil {
		t.Fatal("digest of a file accepted")
	}
}

func TestComponentManifestParsing(t *testing.T) {
	tree := strings.Repeat("ab", 32)
	sum := strings.Repeat("cd", 32)
	good := func() ComponentSet {
		return ComponentSet{Format: 1, Version: "0.2.200", Arch: "arm64", Components: []Component{
			{Name: "core", Asset: "Fennec-0.2.200-macos-arm64-core.zip", Size: 10, SHA256: sum},
			{Name: "chromium", Path: ChromiumComponentPath, Tree: tree, Asset: "Fennec-chromium-abababababababab-macos-arm64.zip", Size: 20, SHA256: "SHA256:" + strings.ToUpper(sum)},
		}}
	}
	encode := func(s ComponentSet) []byte { raw, _ := json.Marshal(s); return raw }
	set, err := ParseReleaseComponents(encode(good()), "0.2.200", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := set.Find("chromium"); c.SHA256 != sum || len(set.Nested()) != 1 {
		t.Fatalf("parsed manifest wrong: %+v", set)
	}
	bad := map[string]func(*ComponentSet){
		"format":  func(s *ComponentSet) { s.Format = 2 },
		"version": func(s *ComponentSet) { s.Version = "0.2.199" },
		"arch":    func(s *ComponentSet) { s.Arch = "amd64" },
		"no core": func(s *ComponentSet) { s.Components = s.Components[1:] },
		"two cores": func(s *ComponentSet) {
			s.Components[1].Name = "core"
			s.Components[1].Path = ""
			s.Components[1].Tree = ""
		},
		"escape":        func(s *ComponentSet) { s.Components[1].Path = "Contents/../../x.app" },
		"absolute":      func(s *ComponentSet) { s.Components[1].Path = "/Contents/Helpers/Chromium.app" },
		"outside":       func(s *ComponentSet) { s.Components[1].Path = "Helpers/Chromium.app" },
		"bad tree":      func(s *ComponentSet) { s.Components[1].Tree = "xyz" },
		"bad sha":       func(s *ComponentSet) { s.Components[0].SHA256 = "" },
		"zero size":     func(s *ComponentSet) { s.Components[0].Size = 0 },
		"asset path":    func(s *ComponentSet) { s.Components[1].Asset = "../x.zip" },
		"duplicate":     func(s *ComponentSet) { s.Components = append(s.Components, s.Components[1]) },
		"core has path": func(s *ComponentSet) { s.Components[0].Path = ChromiumComponentPath },
		"overlap": func(s *ComponentSet) {
			c := s.Components[1]
			c.Name, c.Path, c.Asset = "inner", ChromiumComponentPath+"/Contents/Inner.app", "inner.zip"
			s.Components = append(s.Components, c)
		},
	}
	for name, mutate := range bad {
		s := good()
		mutate(&s)
		if _, err := ParseReleaseComponents(encode(s), "0.2.200", "arm64"); err == nil {
			t.Errorf("%s: invalid manifest accepted", name)
		}
	}
	if _, err := ParseReleaseComponents([]byte("{"), "0.2.200", "arm64"); err == nil {
		t.Error("malformed JSON accepted")
	}
	if _, err := ParseBundleComponents([]byte(sealedManifest(t, tree))); err != nil {
		t.Fatalf("bundle manifest rejected: %v", err)
	}
	if _, err := ParseBundleComponents([]byte(`{"format":1,"components":[{"name":"core"}]}`)); err == nil {
		t.Fatal("bundle manifest naming core accepted")
	}
}

func TestStageReusesInstalledChromiumWhenTreeMatches(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	rel := r.fetch(t)
	progress := &progressLog{}
	staged, reused, plan, err := stageForTest(t, r, rel, installed, progress, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	core, _ := rel.Components.Find(CoreComponent)
	chromium, _ := rel.Components.Find(ChromiumComponent)
	if !plan.Components || plan.Download != core.Size || plan.Saved != chromium.Size || strings.Join(plan.Reused, ",") != "chromium" {
		t.Fatalf("unexpected plan %+v", plan)
	}
	if strings.Join(reused, ",") != "chromium" {
		t.Fatalf("expected chromium reused, got %v", reused)
	}
	if r.hit(chromium.Asset) != 0 || r.hit(AppAssetName("0.2.200", "arm64")) != 0 || r.hit(core.Asset) != 1 {
		t.Fatalf("unexpected downloads: core=%d chromium=%d full=%d", r.hit(core.Asset), r.hit(chromium.Asset), r.hit(AppAssetName("0.2.200", "arm64")))
	}
	if got, err := TreeDigest(staged); err != nil || got != r.referenceDigest(t) {
		t.Fatalf("assembled bundle differs from the release bundle: %v", err)
	}
	if progress.maxDone != core.Size || progress.total != core.Size {
		t.Fatalf("progress must count only downloaded bytes: done=%d total=%d core=%d", progress.maxDone, progress.total, core.Size)
	}
	if !containsStep(progress.steps, StepReuse) {
		t.Fatalf("reuse step not reported: %v", progress.steps)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(staged), "unpack")); !os.IsNotExist(err) {
		t.Fatal("assembly directory left behind")
	}
	// The installed app is untouched.
	if BundleVersion(installed) != "0.2.100" || !dirExists(filepath.Join(installed, ChromiumComponentPath)) {
		t.Fatal("installed bundle modified during staging")
	}
}

func containsStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}

func TestStageDownloadsFullArchiveWhenChromiumChanged(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-2")
	installed := installedFennec(t, "build-1", true)
	rel := r.fetch(t)
	progress := &progressLog{}
	staged, reused, plan, err := stageForTest(t, r, rel, installed, progress, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	full := int64(len(r.files[AppAssetName("0.2.200", "arm64")]))
	if plan.Components || plan.Download != full || len(reused) != 0 {
		t.Fatalf("nothing reusable should mean the full archive: plan=%+v reused=%v", plan, reused)
	}
	if r.hit(AppAssetName("0.2.200", "arm64")) != 1 || r.hit(CoreAssetName("0.2.200", "arm64")) != 0 {
		t.Fatal("expected only the full archive to download")
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}
}

func TestStageDownloadsComponentWhenCloneFails(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	rel := r.fetch(t)
	orig := cloneTree
	cloneTree = func(src, dst string) error { return errors.New("clone unavailable") }
	defer func() { cloneTree = orig }()
	progress := &progressLog{}
	staged, reused, _, err := stageForTest(t, r, rel, installed, progress, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	core, _ := rel.Components.Find(CoreComponent)
	chromium, _ := rel.Components.Find(ChromiumComponent)
	if len(reused) != 0 || r.hit(chromium.Asset) != 1 || r.hit(AppAssetName("0.2.200", "arm64")) != 0 {
		t.Fatalf("expected the chromium component download: reused=%v hits=%d", reused, r.hit(chromium.Asset))
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}
	if progress.maxDone != core.Size+chromium.Size || progress.total != core.Size+chromium.Size {
		t.Fatalf("progress should grow to the unplanned download: done=%d total=%d", progress.maxDone, progress.total)
	}
}

func TestStageDownloadsComponentWhenInstalledCopyIsModified(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	// components.json still claims build-1, but the content was altered.
	if err := os.WriteFile(filepath.Join(installed, ChromiumComponentPath, "Contents/MacOS/Chromium"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := r.fetch(t)
	staged, reused, plan, err := stageForTest(t, r, rel, installed, &progressLog{}, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	chromium, _ := rel.Components.Find(ChromiumComponent)
	if len(plan.Reused) != 1 || len(reused) != 0 || r.hit(chromium.Asset) != 1 {
		t.Fatalf("modified installed copy must be downloaded instead: plan=%+v reused=%v", plan, reused)
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}
}

func TestStageFallsBackToFullArchiveOnCorruptComponent(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	chromium, _ := r.manifest.Find(ChromiumComponent)
	// An archive whose bytes match the manifest digest but whose content is
	// not the tree the signed core expects.
	wrong := zipTree(t, "Chromium.app", chromiumFiles("evil"))
	r.files[chromium.Asset] = wrong
	r.manifest.Components[1].Size = int64(len(wrong))
	r.manifest.Components[1].SHA256 = sum256(wrong)
	r.publishManifest()
	rel := r.fetch(t)
	orig := cloneTree
	cloneTree = func(string, string) error { return errors.New("no clone") }
	defer func() { cloneTree = orig }()
	staged, reused, _, err := stageForTest(t, r, rel, installed, &progressLog{}, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	if len(reused) != 0 || r.hit(AppAssetName("0.2.200", "arm64")) != 1 {
		t.Fatal("corrupt component should fall back to the full archive")
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}
}

func TestStageFallsBackWhenComponentDigestMismatches(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	core, _ := r.manifest.Find(CoreComponent)
	// The core archive on the server is not the one the manifest names.
	r.files[core.Asset] = append([]byte{}, r.files[core.Asset]...)
	r.files[core.Asset][len(r.files[core.Asset])-1] ^= 0xff
	r.digests[core.Asset] = core.SHA256
	rel := r.fetch(t)
	staged, _, _, err := stageForTest(t, r, rel, installed, &progressLog{}, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	if r.hit(AppAssetName("0.2.200", "arm64")) != 1 {
		t.Fatal("expected full archive after core digest mismatch")
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}
}

func TestStageFallsBackWhenFeedDisagreesWithSealedManifest(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	// Feed claims the installed build-1 tree is still current, but the signed
	// core expects a different Chromium.
	other := treeOf(t, chromiumFiles("build-9"))
	core := coreFiles(t, "0.2.200", other)
	coreZip := zipTree(t, "Fennec.app", core)
	r.files[r.manifest.Components[0].Asset] = coreZip
	r.manifest.Components[0].Size = int64(len(coreZip))
	r.manifest.Components[0].SHA256 = sum256(coreZip)
	r.publishManifest()
	rel := r.fetch(t)
	_, reused, _, err := stageForTest(t, r, rel, installed, &progressLog{}, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	if len(reused) != 0 || r.hit(AppAssetName("0.2.200", "arm64")) != 1 {
		t.Fatal("disagreeing feed must fall back to the full archive")
	}
}

func TestStageFallsBackWhenAssembledSignatureFails(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	rel := r.fetch(t)
	var calls atomic.Int32
	verify := func(_ context.Context, _, candidate string) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("signature invalid")
		}
		return nil
	}
	staged, reused, _, err := stageForTest(t, r, rel, installed, &progressLog{}, verify)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(reused) != 0 || r.hit(AppAssetName("0.2.200", "arm64")) != 1 {
		t.Fatalf("expected full-archive retry after a failed signature check (calls=%d)", calls.Load())
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}

	// When the full archive fails too, nothing is left behind.
	r2 := newComponentRelease(t, "0.2.200", "build-1")
	installed2 := installedFennec(t, "build-1", true)
	_, _, _, err = stageForTest(t, r2, r2.fetch(t), installed2, &progressLog{}, func(context.Context, string, string) error { return errors.New("bad") })
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, statErr := os.Stat(stagingRoot(installed2)); !os.IsNotExist(statErr) {
		t.Fatal("failed staging left files behind")
	}
}

func TestReleaseWithoutComponentsUsesFullArchive(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	r.omit[ComponentsAssetName("0.2.200", "arm64")] = true
	installed := installedFennec(t, "build-1", true)
	rel := r.fetch(t)
	if rel.Components != nil {
		t.Fatal("components found in a release without a manifest")
	}
	_, reused, plan, err := stageForTest(t, r, rel, installed, &progressLog{}, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Components || len(reused) != 0 || r.hit(AppAssetName("0.2.200", "arm64")) != 1 {
		t.Fatal("old release should download the full archive")
	}
}

func TestComponentManifestDigestMismatchIsIgnored(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	r.digests[ComponentsAssetName("0.2.200", "arm64")] = strings.Repeat("0", 64)
	rel, err := FetchLatest(context.Background(), r.srv.Client(), r.srv.URL+"/latest")
	if err != nil {
		t.Fatal(err)
	}
	if set, err := FetchComponents(context.Background(), r.srv.Client(), rel, "arm64"); err == nil || set != nil {
		t.Fatal("tampered component manifest accepted")
	}
}

func TestServiceReportsIncrementalSizeAndReuse(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	svc := NewService(Config{
		Bundle:       installed,
		Current:      "0.2.100",
		Arch:         "arm64",
		Feed:         func() string { return r.srv.URL + "/latest" },
		HTTPClient:   r.srv.Client(),
		Transport:    r.srv.Client().Transport,
		VerifySigner: acceptSigner,
		ApplyUpdate: func(_ context.Context, stagedApp, dest string) error {
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
			return os.Rename(stagedApp, dest)
		},
	})
	ctx := context.Background()
	found := svc.Check(ctx, false)
	core, _ := r.manifest.Find(CoreComponent)
	chromium, _ := r.manifest.Find(ChromiumComponent)
	if found.State != "found" || found.Size != core.Size || found.Saved != chromium.Size || strings.Join(found.Reused, ",") != "chromium" {
		t.Fatalf("found snapshot should describe the incremental download: %+v", found)
	}
	ready := svc.Download(ctx)
	if ready.State != "ready" || strings.Join(ready.Reused, ",") != "chromium" || ready.Total != core.Size || ready.Done != core.Size {
		t.Fatalf("ready snapshot: %+v", ready)
	}
	if _, err := svc.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := TreeDigest(installed); got != r.referenceDigest(t) {
		t.Fatal("installed bundle differs from the release bundle")
	}
}

func TestStageRefusesSymlinkedComponentParent(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	outside := t.TempDir()
	// A core whose Contents/Helpers points outside the staged bundle.
	core := coreFiles(t, "0.2.200", r.manifest.Components[1].Tree)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, f := range core {
		hdr := &zip.FileHeader{Name: "Fennec.app/" + name, Method: zip.Deflate}
		hdr.SetMode(0o644)
		if f.exec {
			hdr.SetMode(0o755)
		}
		w, _ := zw.CreateHeader(hdr)
		_, _ = w.Write([]byte(f.data))
	}
	link := &zip.FileHeader{Name: "Fennec.app/Contents/Helpers", Method: zip.Store}
	link.SetMode(os.ModeSymlink | 0o777)
	w, _ := zw.CreateHeader(link)
	_, _ = w.Write([]byte(outside))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	coreZip := buf.Bytes()
	r.files[r.manifest.Components[0].Asset] = coreZip
	r.manifest.Components[0].Size = int64(len(coreZip))
	r.manifest.Components[0].SHA256 = sum256(coreZip)
	r.publishManifest()
	staged, _, _, err := stageForTest(t, r, r.fetch(t), installed, &progressLog{}, acceptSigner)
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("component written outside the staged bundle: %v", entries)
	}
	if r.hit(AppAssetName("0.2.200", "arm64")) != 1 {
		t.Fatal("expected the full archive after a refused core")
	}
	if got, _ := TreeDigest(staged); got != r.referenceDigest(t) {
		t.Fatal("staged bundle differs from the release bundle")
	}
}
