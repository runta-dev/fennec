//go:build darwin

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, out)
	}
}

func signedFixtureApp(t *testing.T, app, id, version string, extra map[string]string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(app, "Contents/MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + id + `</string><key>CFBundleExecutable</key><string>main</string><key>CFBundleShortVersionString</key><string>` + version + `</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, "/bin/cp", "/usr/bin/true", filepath.Join(app, "Contents/MacOS/main"))
	for name, data := range extra {
		p := filepath.Join(app, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

// TestSignedBundleReassembledFromCoreAndReusedChromiumVerifies builds a real
// (ad-hoc) signed Fennec.app with a separately signed nested Chromium.app,
// publishes it as core + chromium components, and checks that staging from
// the core plus a clone of the installed Chromium passes codesign --deep
// --strict: the outer seal over the nested bundle survives reassembly.
func TestSignedBundleReassembledFromCoreAndReusedChromiumVerifies(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := os.MkdirTemp(home, ".fennec-components-signed-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sandbox)

	build := func(dir, version string) string {
		app := signedFixtureApp(t, filepath.Join(dir, "Fennec.app"), "com.runta.fennec", version, map[string]string{"Contents/Resources/notes.txt": version})
		chromium := signedFixtureApp(t, filepath.Join(app, ChromiumComponentPath), "org.chromium.Chromium", "160.0", map[string]string{"Contents/Resources/credits.html": strings.Repeat("c", 64<<10)})
		run(t, "/usr/bin/codesign", "--force", "--sign", "-", chromium)
		tree, err := TreeDigest(chromium)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(ComponentSet{Format: ComponentsFormat, Components: []Component{{Name: ChromiumComponent, Path: ChromiumComponentPath, Tree: tree}}})
		if err := os.WriteFile(filepath.Join(app, BundleComponentsPath), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		// Outer signature without --deep seals the nested signature.
		run(t, "/usr/bin/codesign", "--force", "--sign", "-", app)
		run(t, "/usr/bin/codesign", "--verify", "--deep", "--strict", app)
		return app
	}
	releaseApp := build(filepath.Join(sandbox, "release"), "0.2.200")
	// The installed app carries the very same signed Chromium (as releases do
	// when the pin is unchanged): ditto it in from the release build.
	installedApp := signedFixtureApp(t, filepath.Join(sandbox, "installed", "Fennec.app"), "com.runta.fennec", "0.2.100", map[string]string{"Contents/Resources/notes.txt": "0.2.100"})
	run(t, "/usr/bin/ditto", filepath.Join(releaseApp, ChromiumComponentPath), filepath.Join(installedApp, ChromiumComponentPath))
	run(t, "/bin/cp", filepath.Join(releaseApp, BundleComponentsPath), filepath.Join(installedApp, BundleComponentsPath))
	run(t, "/usr/bin/codesign", "--force", "--sign", "-", installedApp)

	// Release archives, made the way release-components does.
	out := filepath.Join(sandbox, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	coreApp := filepath.Join(sandbox, "core", "Fennec.app")
	if err := os.MkdirAll(filepath.Dir(coreApp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CloneTree(releaseApp, coreApp); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(coreApp, ChromiumComponentPath)); err != nil {
		t.Fatal(err)
	}
	sealed, err := ReadBundleComponents(releaseApp)
	if err != nil {
		t.Fatal(err)
	}
	chromiumMeta, _ := sealed.Find(ChromiumComponent)
	files := map[string][]byte{}
	zipDir := func(src, name string) Component {
		dest := filepath.Join(out, name)
		run(t, "/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", src, dest)
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = data
		return Component{Asset: name, Size: int64(len(data)), SHA256: sum256(data)}
	}
	zipDir(releaseApp, AppAssetName("0.2.200", "arm64"))
	core := zipDir(coreApp, CoreAssetName("0.2.200", "arm64"))
	core.Name = CoreComponent
	chromium := zipDir(filepath.Join(releaseApp, ChromiumComponentPath), NestedAssetName(ChromiumComponent, chromiumMeta.Tree, "arm64"))
	chromium.Name, chromium.Path, chromium.Tree = ChromiumComponent, ChromiumComponentPath, chromiumMeta.Tree
	manifest, _ := json.Marshal(ComponentSet{Format: ComponentsFormat, Version: "0.2.200", Arch: "arm64", Components: []Component{core, chromium}})
	files[ComponentsAssetName("0.2.200", "arm64")] = manifest

	var hitsMu sync.Mutex
	hits := map[string]int{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			assets := map[string]any{}
			for name, data := range files {
				assets[name] = map[string]any{"url": srv.URL + "/dl/" + name, "size": len(data), "sha256": sum256(data)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.2.200", "assets": assets})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		hitsMu.Lock()
		hits[name]++
		hitsMu.Unlock()
		_, _ = w.Write(files[name])
	}))
	defer srv.Close()

	rel, err := FetchLatest(context.Background(), srv.Client(), srv.URL+"/latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Components, err = FetchComponents(context.Background(), srv.Client(), rel, "arm64"); err != nil || rel.Components == nil {
		t.Fatalf("components: %v", err)
	}
	codesignOnly := func(ctx context.Context, _, candidate string) error {
		if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", candidate).CombinedOutput(); err != nil {
			return fmt.Errorf("%s", out)
		}
		return nil
	}
	plan := PlanUpdate(rel, installedApp, "arm64")
	staged, reused, err := stageReleaseBundle(context.Background(), srv.Client().Transport, rel, installedApp, "arm64", plan, progressOptions{}, codesignOnly)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reused, ",") != "chromium" || hits[chromium.Asset] != 0 || hits[AppAssetName("0.2.200", "arm64")] != 0 {
		t.Fatalf("expected Chromium reuse without downloads: reused=%v hits=%v", reused, hits)
	}
	want, _ := TreeDigest(releaseApp)
	if got, _ := TreeDigest(staged); got != want {
		t.Fatal("reassembled bundle differs from the signed release bundle")
	}
	if err := codesignOnly(context.Background(), "", staged); err != nil {
		t.Fatalf("reassembled bundle fails codesign: %v", err)
	}

	// A Chromium signed again (same content, new signature) is a different
	// tree, so it is never mixed into a bundle sealed for the other one.
	resigned := filepath.Join(sandbox, "resigned.app")
	run(t, "/usr/bin/ditto", filepath.Join(installedApp, ChromiumComponentPath), resigned)
	run(t, "/usr/bin/codesign", "--force", "--sign", "-", "--identifier", "org.chromium.Other", resigned)
	if other, _ := TreeDigest(resigned); other == chromiumMeta.Tree {
		t.Fatal("re-signed Chromium kept the same tree digest")
	}
}
