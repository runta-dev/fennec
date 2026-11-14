// Command cua-package stages fennec-cua and the pinned open-source
// Chromium for the Fennec bundle:
//
//	cua-package -stage DIR [-cache DIR] [-identity NAME|SHA1|-] [-keychain PATH] [-skip-build]
//	cua-package -smoke PATH/Chromium.app
//
// Chromium is pinned for Apple silicon only; on any other architecture it
// stops with an error.
//
// It downloads the pinned chromium-browser-snapshots build (or reuses the
// cached archive), checks its size and SHA-256 before anything else,
// extracts Chromium.app, records Chromium's licence and the generated
// third-party notices (also served as chrome://credits) inside the app and in
// DIR/licenses, signs the app inside out with the Fennec signing identity
// (hardened runtime with Chromium's per-helper entitlements), verifies the
// signature, starts the signed browser once headless, builds and signs
// fennec-cua, and writes DIR/MANIFEST.json:
//
//	DIR/fennec-cua         -> Fennec.app/Contents/MacOS/fennec-cua
//	DIR/Chromium.app       -> Fennec.app/Contents/Helpers/Chromium.app
//	DIR/licenses/*         -> Fennec.app/Contents/Resources/licenses/
//
// It never installs anything, never creates or changes certificates, and
// stops (instead of waiting) when signing would ask the user for the key.
// scripts/build-macos.sh runs it with -skip-build, caches the result and
// builds fennec-cua itself; -smoke repeats the browser checks on a shipped
// copy (scripts/verify-macos-package.sh).
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"fennec/cua/internal/cdp"
	"fennec/cua/internal/chromium"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cua-package:", err)
		os.Exit(1)
	}
}

type options struct {
	stage, cache, identity, keychain   string
	creditsCache, creditsOut, smokeApp string
	skipBuild, allowNoCredits          bool
}

func run(args []string) error {
	var o options
	fs := flag.NewFlagSet("cua-package", flag.ContinueOnError)
	fs.StringVar(&o.stage, "stage", "", "staging directory (created; must be empty or absent)")
	fs.StringVar(&o.cache, "cache", "", "download cache (default: the user cache directory)")
	fs.StringVar(&o.identity, "identity", envOr("FENNEC_SIGN_IDENTITY", "Fennec Local Development"), `signing identity name or SHA-1 fingerprint, or "-" for ad-hoc`)
	fs.StringVar(&o.keychain, "keychain", envOr("FENNEC_SIGN_KEYCHAIN", filepath.Join(os.Getenv("HOME"), "Library/Keychains/login.keychain-db")), "keychain holding the identity")
	fs.BoolVar(&o.skipBuild, "skip-build", false, "stage Chromium only")
	fs.BoolVar(&o.allowNoCredits, "allow-missing-credits", false, "skip generating the third-party notices: chrome://credits stays the sample page (development only; not for distribution)")
	fs.StringVar(&o.creditsCache, "credits-cache", "", "cache of Chromium source metadata for the notices (default: the user cache directory)")
	fs.StringVar(&o.creditsOut, "credits-out", "", "only generate the pinned commit's notices into this directory and print their hash (for a pin bump)")
	fs.StringVar(&o.smokeApp, "smoke", "", "only check an already staged or installed Chromium.app (for example inside Fennec.app): signature, JavaScript in a renderer, and chrome://credits")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.smokeApp != "" {
		return smokeOnly(context.Background(), o.smokeApp)
	}
	ucache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	if o.cache == "" {
		o.cache = filepath.Join(ucache, "fennec", "cua", "chromium-downloads")
	}
	if o.creditsCache == "" {
		o.creditsCache = filepath.Join(ucache, "fennec", "cua", "chromium-source")
	}
	// Fennec ships for Apple silicon only: one pin, built and smoke-tested
	// on an arm64 Mac.
	build, err := chromium.BuildFor(runtime.GOARCH)
	if err != nil {
		return err
	}
	if o.creditsOut != "" {
		return creditsOnly(context.Background(), build, o.creditsCache, o.creditsOut)
	}
	if o.stage == "" {
		return errors.New("-stage is required")
	}
	if entries, err := os.ReadDir(o.stage); err == nil && len(entries) > 0 {
		return fmt.Errorf("the staging directory %s is not empty", o.stage)
	}
	if err := os.MkdirAll(filepath.Join(o.stage, "licenses"), 0o755); err != nil {
		return err
	}
	ctx := context.Background()

	step("fetching Chromium r%s (%s, %s)", build.Revision, build.Version, build.Arch)
	archive, err := fetch(ctx, build, o.cache)
	if err != nil {
		return err
	}
	step("extracting Chromium.app")
	app := filepath.Join(o.stage, chromium.AppName)
	if err := unzipApp(archive, o.stage); err != nil {
		return err
	}

	step("recording Chromium's licence")
	license, err := fetchLicense(ctx, build, o.cache)
	if err != nil {
		return err
	}
	files := map[string][]byte{"Chromium-LICENSE.txt": license}
	// Snapshot builds are made without generate_about_credits: their
	// chrome://credits is a placeholder. The notices are generated from
	// the pinned commit's sources (internal/credits) and put both into
	// licenses/ and in place of the placeholder resource.
	var cf *creditsFiles
	creditsNote := "MISSING: -allow-missing-credits skipped the notices; not distributable"
	if o.allowNoCredits {
		step("WARNING: no third-party notices; staged for development only, not for distribution")
	} else {
		if cf, err = buildCredits(ctx, build, o.creditsCache); err != nil {
			return err
		}
		id, err := patchCreditsPage(ctx, app, build.Commit, cf.HTML)
		if err != nil {
			return fmt.Errorf("replacing the placeholder chrome://credits: %w", err)
		}
		step("chrome://credits now carries the %d generated notices (resource %d)", cf.Components, id)
		files["Chromium-credits.html"], files["Chromium-credits.txt"] = cf.HTML, cf.Text
		creditsNote = fmt.Sprintf("licenses/Chromium-credits.html and .txt (%d components, SHA-256 %s, generated from the pinned commit by internal/credits; also chrome://credits)", cf.Components, build.CreditsSHA256)
	}
	res := filepath.Join(app, "Contents", "Resources")
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(o.stage, "licenses", name), data, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(res, name), data, 0o644); err != nil {
			return err
		}
	}
	step("naming the cookie key's Keychain item %q", chromium.SafeStorageService)
	if err := patchSafeStorage(app); err != nil {
		return fmt.Errorf("renaming the Safe Storage item: %w", err)
	}

	noTeam := false
	ident, err := resolveIdentity(o.identity, o.keychain)
	if err != nil {
		return err
	}
	if ident != "-" {
		step("checking that signing runs without a prompt")
		team, err := probeSign(ident, o.keychain)
		if err != nil {
			return err
		}
		// A certificate without a team (the self-signed development one)
		// fails library validation between Chromium and its own framework.
		noTeam = team == ""
	}
	step("signing Chromium.app (%s)", identityLabel(o.identity, ident))
	if err := signApp(app, ident, o.keychain, noTeam); err != nil {
		return err
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("the signature does not verify: %v: %s", err, out)
	}
	step("starting the signed Chromium once")
	product, err := smoke(ctx, chromium.Executable(app))
	if err != nil {
		return fmt.Errorf("the signed Chromium does not start: %w", err)
	}
	if cf != nil {
		step("checking chrome://credits")
		page, err := dumpCredits(ctx, chromium.Executable(app))
		if err != nil {
			return fmt.Errorf("reading chrome://credits: %w", err)
		}
		if n := bytes.Count(page, []byte(`<div class="product">`)); n != cf.Components {
			return fmt.Errorf("chrome://credits shows %d components; %d were generated", n, cf.Components)
		}
	}
	step("checking the Keychain item with a temporary keychain")
	if err := checkSafeStorage(ctx, chromium.Executable(app)); err != nil {
		return fmt.Errorf("the Safe Storage check: %w", err)
	}

	if !o.skipBuild {
		step("building fennec-cua")
		bin := filepath.Join(o.stage, "fennec-cua")
		cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/fennec-cua")
		cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+build.Arch, "CGO_ENABLED=1")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build: %w", err)
		}
		if err := sign(bin, ident, o.keychain, ""); err != nil {
			return err
		}
	}
	manifest := map[string]any{
		"chromium": map[string]any{"revision": build.Revision, "commit": build.Commit, "version": build.Version, "arch": build.Arch,
			"sha256": build.SHA256, "url": build.URL(), "product": product, "license": "BSD-3-Clause (licenses/Chromium-LICENSE.txt)", "thirdPartyNotices": creditsNote,
			"modified": "chrome://credits resource replaced by the generated notices; Safe Storage Keychain item renamed to \"" + chromium.SafeStorageService + "\"; re-signed"},
		"signed": identityLabel(o.identity, ident),
		"layout": map[string]string{"fennec-cua": "Contents/MacOS/fennec-cua", "Chromium.app": "Contents/Helpers/Chromium.app", "licenses": "Contents/Resources/licenses"},
		"at":     time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(o.stage, "MANIFEST.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	step("staged in %s", o.stage)
	return nil
}

// smokeOnly is -smoke: the checks a staging runs on its signed Chromium,
// repeated on an app where it is shipped. The browser runs headless on a
// throwaway profile with the mock keychain.
func smokeOnly(ctx context.Context, app string) error {
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("the signature does not verify: %v: %s", err, out)
	}
	bin := chromium.Executable(app)
	product, err := smoke(ctx, bin)
	if err != nil {
		return fmt.Errorf("the Chromium in %s does not start: %w", app, err)
	}
	step("%s runs JavaScript in a renderer (%s)", app, product)
	html, err := os.ReadFile(filepath.Join(app, "Contents", "Resources", "Chromium-credits.html"))
	if err != nil {
		return fmt.Errorf("the app carries no third-party notices: %w", err)
	}
	want := bytes.Count(html, []byte(`<div class="product">`))
	page, err := dumpCredits(ctx, bin)
	if err != nil {
		return fmt.Errorf("reading chrome://credits: %w", err)
	}
	if n := bytes.Count(page, []byte(`<div class="product">`)); n != want || want == 0 {
		return fmt.Errorf("chrome://credits shows %d components; the bundled notices have %d", n, want)
	}
	step("chrome://credits shows the %d bundled components", want)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func step(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cua-package: "+format+"\n", args...)
}

// fetch returns the cached archive, downloading it when missing; either
// way its size and SHA-256 must match the pin.
func fetch(ctx context.Context, b chromium.Build, cache string) (string, error) {
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(cache, fmt.Sprintf("chromium-%s-%s.zip", b.Bucket, b.Revision))
	if err := verify(path, b); err == nil {
		return path, nil
	}
	tmp := path + ".part"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", b.URL(), resp.Status)
	}
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, b.Size+1)); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := verify(tmp, b); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func verify(path string, b chromium.Build) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != b.Size {
		return fmt.Errorf("%s is %d bytes; the pin says %d", path, n, b.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {
		return fmt.Errorf("%s has SHA-256 %s; the pin says %s", path, got, b.SHA256)
	}
	return nil
}

// unzipApp extracts chrome-mac/Chromium.app into dir, keeping symbolic
// links and modes, refusing paths that leave the app.
func unzipApp(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	const prefix = "chrome-mac/Chromium.app/"
	found := false
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		found = true
		rel := filepath.FromSlash(strings.TrimPrefix(f.Name, "chrome-mac/"))
		if rel != filepath.Clean(rel) && rel != filepath.Clean(rel)+string(filepath.Separator) || strings.Contains(rel, "..") {
			return fmt.Errorf("refusing %q in the archive", f.Name)
		}
		dst := filepath.Join(dir, rel)
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			t := string(target)
			if filepath.IsAbs(t) || !strings.HasPrefix(filepath.Clean(filepath.Join(filepath.Dir(dst), t)), filepath.Join(dir, chromium.AppName)) {
				return fmt.Errorf("refusing link %q -> %q", f.Name, t)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(t, dst); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()|0o200)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			rc.Close()
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
	if !found {
		return errors.New("the archive holds no chrome-mac/Chromium.app")
	}
	return nil
}

// fetchLicense reads Chromium's LICENSE at the build's commit, from the
// download cache when an earlier run kept it there, so a warm cache stages
// without network.
func fetchLicense(ctx context.Context, b chromium.Build, cache string) ([]byte, error) {
	if b.Commit == "" {
		return nil, errors.New("the pin has no commit for the licence")
	}
	cached := filepath.Join(cache, "chromium-LICENSE-"+b.Commit+".txt")
	if text, err := os.ReadFile(cached); err == nil && bytes.Contains(text, []byte("The Chromium Authors")) {
		return text, nil
	}
	text, err := downloadLicense(ctx, b)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}
	return text, os.WriteFile(cached, text, 0o644)
}

func downloadLicense(ctx context.Context, b chromium.Build) ([]byte, error) {
	u := "https://chromium.googlesource.com/chromium/src/+/" + b.Commit + "/LICENSE?format=TEXT"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("licence %s: %s", u, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	text, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(text, []byte("The Chromium Authors")) {
		return nil, errors.New("the licence text is not Chromium's")
	}
	return text, nil
}

// headless runs the given Chromium on a throwaway profile.
func headless(ctx context.Context, bin string, fn func(c *cdp.Conn) error) error {
	dir, err := os.MkdirTemp("", "cua-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// The mock keychain: these runs never touch the user's Keychain.
	// The first start from a freshly mounted disk image waits while macOS
	// assesses the whole bundle, minutes on a slow CI runner.
	b := chromium.New(chromium.Options{Bin: bin, ProfileDir: dir, Headless: true, MockKeychain: true, IdleTimeout: -1, StartTimeout: 5 * time.Minute})
	defer b.Close()
	c, _, err := b.Conn(ctx)
	if err != nil {
		return err
	}
	return fn(c)
}

// dumpCredits renders chrome://credits (the third-party notices compiled
// into this build) and returns the page.
func dumpCredits(ctx context.Context, bin string) ([]byte, error) {
	var out []byte
	err := headless(ctx, bin, func(c *cdp.Conn) error {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		v, err := c.Call(cctx, "", "Target.createTarget", map[string]any{"url": "chrome://credits/"})
		if err != nil {
			return err
		}
		var t struct {
			TargetID string `json:"targetId"`
		}
		_ = json.Unmarshal(v, &t)
		v, err = c.Call(cctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true})
		if err != nil {
			return err
		}
		var a struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(v, &a)
		for {
			v, err = c.Call(cctx, a.SessionID, "Runtime.evaluate", map[string]any{"returnByValue": true,
				"expression": `document.readyState === "complete" && (document.querySelectorAll(".product").length > 10 || !!document.getElementById("print-link")) ? "<!doctype html>\n" + document.documentElement.outerHTML : ""`})
			if err != nil {
				return err
			}
			var r struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			_ = json.Unmarshal(v, &r)
			if r.Result.Value != "" {
				out = []byte(r.Result.Value)
				return nil
			}
			select {
			case <-time.After(200 * time.Millisecond):
			case <-cctx.Done():
				return errors.New("chrome://credits did not render")
			}
		}
	})
	return out, err
}

func smoke(ctx context.Context, bin string) (string, error) {
	var product string
	err := headless(ctx, bin, func(c *cdp.Conn) error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		v, err := c.Call(cctx, "", "Target.createTarget", map[string]any{"url": "about:blank"})
		if err != nil {
			return err
		}
		var t struct {
			TargetID string `json:"targetId"`
		}
		_ = json.Unmarshal(v, &t)
		// A renderer that cannot JIT (missing entitlements) never answers:
		// run hot JavaScript in the page and read the result.
		v, err = c.Call(cctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true})
		if err != nil {
			return err
		}
		var a struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(v, &a)
		jctx, jcancel := context.WithTimeout(cctx, 15*time.Second)
		v, err = c.Call(jctx, a.SessionID, "Runtime.evaluate", map[string]any{"returnByValue": true, "expression": "(() => { let x = 0; for (let i = 0; i < 5e7; i++) x = (x + i) % 1000003; return x; })()"})
		jcancel()
		if err != nil {
			return fmt.Errorf("the page's renderer did not run JavaScript: %w", err)
		}
		v, err = c.Call(cctx, "", "Browser.getVersion", nil)
		if err != nil {
			return err
		}
		var ver struct {
			Product string `json:"product"`
		}
		_ = json.Unmarshal(v, &ver)
		product = ver.Product
		return nil
	})
	return product, err
}

var (
	identityLine = regexp.MustCompile(`^\s*\d+\)\s+([0-9A-F]{40})\s+"([^"]+)"`)
	fingerprint  = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)
)

// resolveIdentity finds the identity's certificate hash in the keychain,
// as scripts/build-macos.sh does (a self-signed development certificate is
// valid for signing although not trusted for distribution).
func resolveIdentity(name, keychain string) (string, error) {
	if name == "-" {
		return "-", nil
	}
	// scripts/build-macos.sh passes the fingerprint it already resolved.
	if fingerprint.MatchString(name) {
		return strings.ToUpper(name), nil
	}
	out, err := exec.Command("security", "find-identity", "-p", "codesigning", keychain).Output()
	if err != nil {
		return "", fmt.Errorf("security find-identity: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if m := identityLine.FindStringSubmatch(line); m != nil && m[2] == name {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no signing identity %q in %s; create it with scripts/create-development-identity.sh (this command never creates one)", name, keychain)
}

func identityLabel(name, hash string) string {
	if hash == "-" {
		return "ad-hoc"
	}
	return name
}

// probeSign signs a scratch copy of a small binary with a deadline: a
// signature that waits on a Keychain dialog is stopped and reported. It
// answers the identity's team ("" for a certificate without one).
func probeSign(ident, keychain string) (string, error) {
	dir, err := os.MkdirTemp("", "cua-package-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		return "", err
	}
	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, data, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codesign", "--force", "--keychain", keychain, "--sign", ident, probe).CombinedOutput()
	if ctx.Err() != nil {
		return "", errors.New("signing waited for a Keychain authorisation; allow codesign to use the Fennec signing key once (see skills/fennec-macos-development/SKILL.md) and run again")
	}
	if err != nil {
		return "", fmt.Errorf("codesign: %v: %s", err, out)
	}
	info, _ := exec.Command("codesign", "-dv", probe).CombinedOutput()
	for _, line := range strings.Split(string(info), "\n") {
		if t, ok := strings.CutPrefix(line, "TeamIdentifier="); ok && t != "not set" {
			return t, nil
		}
	}
	return "", nil
}

const libValidation = "com.apple.security.cs.disable-library-validation"

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// entitlementsFor answers the hardened-runtime entitlements of a bundle,
// as Chromium's own signing configuration (and Google Chrome) gives them:
// V8 needs JIT in the renderer and GPU helpers, including their
// "Aperitif" variants; the main app declares the devices pages may ask
// for; other helpers need none.
func entitlementsFor(bundle string) []string {
	switch {
	case bundle == chromium.AppName:
		return []string{"com.apple.security.device.audio-input", "com.apple.security.device.camera", "com.apple.security.device.bluetooth",
			"com.apple.security.device.print", "com.apple.security.device.usb", "com.apple.security.personal-information.location"}
	case strings.HasPrefix(bundle, "Chromium Helper (") && (strings.Contains(bundle, "Renderer)") || strings.Contains(bundle, "GPU)")):
		return []string{"com.apple.security.cs.allow-jit"}
	case strings.HasPrefix(bundle, "Chromium Helper (") && strings.Contains(bundle, "Plugin)"):
		return []string{"com.apple.security.cs.allow-unsigned-executable-memory", libValidation}
	}
	return nil
}

func plist(keys []string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n<plist version=\"1.0\"><dict>\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "<key>%s</key><true/>\n", k)
	}
	b.WriteString("</dict></plist>\n")
	return []byte(b.String())
}

func sign(path, ident, keychain, entFile string) error {
	args := []string{"--force"}
	if ident != "-" {
		args = append(args, "--options", "runtime", "--timestamp", "--keychain", keychain)
	}
	if entFile != "" {
		args = append(args, "--entitlements", entFile)
	}
	args = append(args, "--sign", ident, path)
	out, err := exec.Command("codesign", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %v: %s", path, err, out)
	}
	return nil
}

func isMachO(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	switch hex.EncodeToString(magic[:]) {
	case "cffaedfe", "cefaedfe", "cafebabe", "feedfacf", "feedface":
		return true
	}
	return false
}

// signApp signs inside out: loose Mach-O files, then nested bundles from
// the deepest, then the app.
func signApp(app, ident, keychain string, noTeam bool) error {
	tmp, err := os.MkdirTemp("", "cua-package-ent-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var loose, bundles []string
	err = filepath.WalkDir(app, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if p != app && (strings.HasSuffix(p, ".app") || strings.HasSuffix(p, ".framework")) {
				bundles = append(bundles, p)
			}
			return nil
		}
		if strings.Contains(p, "/Contents/MacOS/") {
			return nil // a bundle's main executable is signed with its bundle
		}
		if isMachO(p) {
			loose = append(loose, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	depth := func(p string) int { return strings.Count(p, string(filepath.Separator)) }
	sort.Slice(loose, func(i, j int) bool { return depth(loose[i]) > depth(loose[j]) })
	sort.Slice(bundles, func(i, j int) bool { return depth(bundles[i]) > depth(bundles[j]) })
	for _, p := range loose {
		if err := sign(p, ident, keychain, ""); err != nil {
			return err
		}
	}
	entFile := func(name string) (string, error) {
		keys := entitlementsFor(name)
		if noTeam && (strings.HasSuffix(name, ".app")) && !contains(keys, libValidation) {
			keys = append(append([]string(nil), keys...), libValidation)
		}
		if len(keys) == 0 || ident == "-" {
			return "", nil
		}
		f := filepath.Join(tmp, strings.NewReplacer(" ", "_", "(", "", ")", "").Replace(name)+".plist")
		return f, os.WriteFile(f, plist(keys), 0o600)
	}
	for _, b := range append(bundles, app) {
		ent, err := entFile(filepath.Base(b))
		if err != nil {
			return err
		}
		if err := sign(b, ident, keychain, ent); err != nil {
			return err
		}
	}
	return nil
}
