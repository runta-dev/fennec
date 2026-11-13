// Command cua-dist builds the standalone fennec-cua distribution: fennec-cua
// without Fennec.app.
//
//	cua-dist -out DIR [-version V] [-chromium-stage DIR | -no-chromium]
//	         [-identity NAME|SHA1|-] [-keychain PATH] [-notarize-profile PROFILE]
//
// It builds FennecCUA.app (layout in internal/standalone) from this
// checkout, the pinned cua-driver release (internal/driver.Pinned, size
// and SHA-256 checked) and a signed Chromium staging (cmd/cua-package
// -stage, run here, or an existing one reused with -chromium-stage, such
// as Fennec's build/cua-cache/KEY), signs it inside out with the identity
// (internal/macsign), verifies it, and writes
// DIR/fennec-cua-V-macos-arm64.tar.gz (-core.tar.gz without Chromium) and
// its .sha256. Before it writes the archive it checks that the signed
// programs start; afterwards it extracts the archive and verifies the
// extracted app. The tarball's entries are sorted, owned by root and dated
// SOURCE_DATE_EPOCH (else the HEAD commit's time); the signatures carry
// their own signing time, so two builds are not byte-identical.
//
// It is a separate command, not a cua-package mode, because
// scripts/build-macos.sh keys its signed Chromium cache on cua-package's
// sources: changing them re-signs Chromium and costs every developer a
// Keychain prompt.
//
// Releases: with a Developer ID Application identity and
// -notarize-profile, the app is submitted with `xcrun notarytool submit
// --wait`, stapled and checked with spctl before it is archived. Without
// it, the archive is for development and for users who clear the
// quarantine attribute themselves (cua/README.md, "Install and use").
//
// Apple silicon only: it stops on any other architecture.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/chromium"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/macsign"
	"fennec/cua/internal/standalone"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cua-dist:", err)
		os.Exit(1)
	}
}

type options struct {
	out, chromiumStage, version, notarizeProfile string
	identity, keychain, cache                    string
	noChromium                                   bool
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func step(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cua-dist: "+format+"\n", args...)
}

func run(args []string) error {
	var o options
	fs := flag.NewFlagSet("cua-dist", flag.ContinueOnError)
	fs.StringVar(&o.out, "out", "", "output directory (created; must be empty or absent)")
	fs.StringVar(&o.version, "version", "0.0.0-dev", "the version stamped into fennec-cua, the app and the archive name")
	fs.StringVar(&o.chromiumStage, "chromium-stage", "", "reuse this signed Chromium staging (a cua-package -stage directory, e.g. ../build/cua-cache/KEY) instead of staging Chromium again")
	fs.BoolVar(&o.noChromium, "no-chromium", false, "leave the browser out (a smaller \"core\" archive; browser calls then fail)")
	fs.StringVar(&o.identity, "identity", envOr("FENNEC_SIGN_IDENTITY", macsign.DefaultIdentity), `signing identity name or SHA-1 fingerprint, or "-" for ad-hoc`)
	fs.StringVar(&o.keychain, "keychain", envOr("FENNEC_SIGN_KEYCHAIN", filepath.Join(os.Getenv("HOME"), "Library/Keychains/login.keychain-db")), "keychain holding the identity")
	fs.StringVar(&o.cache, "cache", "", "download cache for the cua-driver release (default: the user cache directory)")
	fs.StringVar(&o.notarizeProfile, "notarize-profile", "", "notarize and staple the app with this notarytool keychain profile (needs a Developer ID Application identity)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.out == "" || fs.NArg() != 0 {
		return errors.New("usage: cua-dist -out DIR [-version V] [-chromium-stage DIR | -no-chromium] [-identity NAME] [-notarize-profile PROFILE]")
	}
	if o.cache == "" {
		ucache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		o.cache = filepath.Join(ucache, "fennec", "cua", "driver-downloads")
	}
	build, err := chromium.BuildFor(runtime.GOARCH)
	if err != nil {
		return err
	}
	return dist(context.Background(), o, build)
}

var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,39}$`)

func dist(ctx context.Context, o options, build chromium.Build) error {
	if !versionPattern.MatchString(o.version) {
		return fmt.Errorf("version %q is not 1 to 40 letters, digits, '.', '+' or '-'", o.version)
	}
	if o.noChromium && o.chromiumStage != "" {
		return errors.New("-no-chromium and -chromium-stage exclude each other")
	}
	if _, err := os.Stat(filepath.Join("skills", standalone.SkillName, "SKILL.md")); err != nil {
		return fmt.Errorf("run cua-dist from the cua module directory (the skill is read from skills/%s): %w", standalone.SkillName, err)
	}
	out := o.out
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("the output directory %s is not empty", out)
	}
	work := filepath.Join(out, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	step("checking that signing runs without a prompt")
	id, err := macsign.Resolve(o.identity, o.keychain)
	if err != nil {
		return err
	}
	if o.notarizeProfile != "" && id.Team == "" {
		return errors.New("notarization needs a Developer ID Application identity (a certificate with a team); this identity has none")
	}

	// Chromium: a signed staging, verified before it is used.
	var stageManifest map[string]any
	stage := o.chromiumStage
	if !o.noChromium {
		if stage == "" {
			stage = filepath.Join(work, "chromium-stage")
			step("staging Chromium with cua-package (pass -chromium-stage to reuse a staging)")
			cmd := exec.CommandContext(ctx, "go", "run", "./cmd/cua-package", "-stage", stage, "-skip-build", "-identity", o.identity, "-keychain", o.keychain)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("staging Chromium: %w", err)
			}
		}
		if stageManifest, err = checkStage(stage, build); err != nil {
			return err
		}
	}

	rel, err := driver.PinnedFor(build.Arch)
	if err != nil {
		return err
	}
	step("fetching cua-driver %s (%s)", rel.Version, rel.Arch)
	archive, err := fetchDriver(ctx, rel, o.cache)
	if err != nil {
		return err
	}
	driverFiles, err := extractDriver(archive, rel)
	if err != nil {
		return err
	}

	top := "fennec-cua-" + o.version + "-macos-" + build.Arch
	if o.noChromium {
		top += "-core"
	}
	dist := filepath.Join(work, "dist")
	app := filepath.Join(dist, top, standalone.AppName)
	contents := filepath.Join(app, "Contents")
	macosDir := filepath.Join(contents, "MacOS")
	licenses := filepath.Join(contents, "Resources", "licenses")
	for _, d := range []string{macosDir, licenses, filepath.Join(licenses, "go")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	step("assembling %s", standalone.AppName)
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), standalone.InfoPlist(o.version), 0o644); err != nil {
		return err
	}
	icon, err := os.ReadFile(filepath.Join("assets", "FennecCUA.icns"))
	if err != nil {
		return fmt.Errorf("the app icon (go run ./cmd/cua-icon makes it): %w", err)
	}
	if err := os.WriteFile(filepath.Join(contents, "Resources", "FennecCUA.icns"), icon, 0o644); err != nil {
		return err
	}
	for _, name := range []string{"cua-driver", "cua-cursor-theme"} {
		if err := os.WriteFile(filepath.Join(macosDir, name), driverFiles[name], 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(licenses, "cua-driver-LICENSE.txt"), driverFiles["LICENSE"], 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(licenses, "cua-driver-THIRD_PARTY_NOTICES.md"), driverFiles["THIRD_PARTY_NOTICES.md"], 0o644); err != nil {
		return err
	}

	step("building fennec-cua %s", o.version)
	exe := filepath.Join(macosDir, "fennec-cua")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X fennec/cua/internal/engine.Version="+o.version, "-o", exe, "./cmd/fennec-cua")
	cmd.Env = append(os.Environ(), buildEnv(build.Arch)...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}
	mods, err := goLicenses(ctx, build.Arch, filepath.Join(licenses, "go"))
	if err != nil {
		return err
	}
	step("recorded the licences of %d Go modules", mods)

	if err := standalone.CopyTree(filepath.Join("skills", standalone.SkillName), standalone.SkillDir(app)); err != nil {
		return err
	}
	if !o.noChromium {
		step("adding the signed Chromium.app")
		helpers := filepath.Join(contents, "Helpers")
		if err := os.MkdirAll(helpers, 0o755); err != nil {
			return err
		}
		if err := standalone.Ditto(filepath.Join(stage, chromium.AppName), filepath.Join(helpers, chromium.AppName)); err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(stage, "licenses"))
		if err != nil {
			return err
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(stage, "licenses", e.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(licenses, e.Name()), data, 0o644); err != nil {
				return err
			}
		}
	}

	exeSum, err := fileSHA256(exe)
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"name": standalone.DisplayName, "bundleId": standalone.BundleID, "version": o.version, "arch": build.Arch,
		"fennec-cua": map[string]any{"sha256BeforeSigning": exeSum, "source": gitDescribe()},
		"cua-driver": map[string]any{"version": rel.Version, "url": rel.URL, "archiveSha256": rel.SHA256, "license": "MIT (licenses/cua-driver-LICENSE.txt, licenses/cua-driver-THIRD_PARTY_NOTICES.md)"},
		"signed":     id.Label(),
		"notarized":  o.notarizeProfile != "",
		"layout": map[string]string{"fennec-cua": "Contents/MacOS/fennec-cua", "cua-driver": "Contents/MacOS/cua-driver", "skill": "Contents/Resources/skills/" + standalone.SkillName,
			"licenses": "Contents/Resources/licenses"},
	}
	if stageManifest != nil {
		manifest["chromium"] = stageManifest["chromium"]
		manifest["layout"].(map[string]string)["Chromium.app"] = "Contents/Helpers/Chromium.app"
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(contents, "Resources", "MANIFEST.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}

	step("signing %s (%s)", standalone.AppName, id.Label())
	for _, name := range []string{"cua-cursor-theme", "cua-driver"} {
		if err := macsign.Sign(filepath.Join(macosDir, name), id, "", macsign.AppleEvents); err != nil {
			return err
		}
	}
	// The bundle's signature covers its main executable (fennec-cua) and
	// seals everything else; Chromium.app keeps its own signature.
	if err := macsign.Sign(app, id, standalone.BundleID, macsign.AppleEvents); err != nil {
		return err
	}
	if err := verifyApp(ctx, app, o.version, rel); err != nil {
		return err
	}
	if o.notarizeProfile != "" {
		if err := notarize(ctx, app, o.notarizeProfile, work); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dist, top, "INSTALL.txt"), installText(o.version, top, o.notarizeProfile != "", o.noChromium), 0o644); err != nil {
		return err
	}

	tarball := filepath.Join(out, top+".tar.gz")
	step("writing %s", tarball)
	mtime := sourceDate()
	if err := writeTarGz(tarball, dist, top, mtime); err != nil {
		return err
	}
	sum, err := fileSHA256(tarball)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tarball+".sha256", []byte(sum+"  "+filepath.Base(tarball)+"\n"), 0o644); err != nil {
		return err
	}
	step("checking the archive: extracting it and verifying the extracted app")
	check := filepath.Join(work, "check")
	if err := os.MkdirAll(check, 0o755); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "/usr/bin/tar", "-xzf", tarball, "-C", check).CombinedOutput(); err != nil {
		return fmt.Errorf("extracting the archive: %v: %s", err, out)
	}
	if err := verifyApp(ctx, filepath.Join(check, top, standalone.AppName), o.version, rel); err != nil {
		return fmt.Errorf("the extracted app: %w", err)
	}
	st, err := os.Stat(tarball)
	if err != nil {
		return err
	}
	step("done: %s (%.1f MB, SHA-256 %s)", tarball, float64(st.Size())/1e6, sum)
	return nil
}

// buildEnv is the environment for building fennec-cua as Fennec.app does.
func buildEnv(arch string) []string {
	return []string{"GOOS=darwin", "GOARCH=" + arch, "CGO_ENABLED=1", "MACOSX_DEPLOYMENT_TARGET=13.0", "CGO_CFLAGS=-O2 -g -mmacosx-version-min=13.0"}
}

// checkStage verifies a Chromium staging: its manifest names the pinned
// build and its app's signature verifies.
func checkStage(stage string, build chromium.Build) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(stage, "MANIFEST.json"))
	if err != nil {
		return nil, fmt.Errorf("%s is not a Chromium staging: %w", stage, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	c, _ := m["chromium"].(map[string]any)
	if c == nil || c["revision"] != build.Revision {
		return nil, fmt.Errorf("the staging %s is not of the pinned Chromium r%s", stage, build.Revision)
	}
	if notes, _ := c["thirdPartyNotices"].(string); strings.HasPrefix(notes, "MISSING") {
		return nil, fmt.Errorf("the staging %s has no third-party notices (staged with -allow-missing-credits); not distributable", stage)
	}
	app := filepath.Join(stage, chromium.AppName)
	if err := macsign.VerifyDeep(app); err != nil {
		return nil, fmt.Errorf("the staged Chromium: %w", err)
	}
	return m, nil
}

// fetchDriver returns the cached cua-driver archive, downloading it when
// missing; either way its size and SHA-256 must match the pin.
func fetchDriver(ctx context.Context, r driver.Release, cache string) (string, error) {
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(cache, r.TopDir()+".tar.gz")
	if err := verifyDriver(path, r); err == nil {
		return path, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", r.URL, resp.Status)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, r.Size+1)); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := verifyDriver(tmp, r); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func verifyDriver(path string, r driver.Release) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() != r.Size {
		return fmt.Errorf("%s is %d bytes; the pin says %d", path, st.Size(), r.Size)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if sum != r.SHA256 {
		return fmt.Errorf("%s has SHA-256 %s; the pin says %s", path, sum, r.SHA256)
	}
	return nil
}

// extractDriver reads the pinned files from the driver archive; anything
// else in it is ignored, and a pinned file that is missing is an error.
func extractDriver(archive string, r driver.Release) (map[string][]byte, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range r.Files {
		want[r.TopDir()+"/"+n] = true
	}
	files := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !want[h.Name] || h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 256<<20))
		if err != nil {
			return nil, err
		}
		files[strings.TrimPrefix(h.Name, r.TopDir()+"/")] = data
	}
	for _, n := range r.Files {
		if files[n] == nil {
			return nil, fmt.Errorf("the cua-driver archive lacks %s/%s", r.TopDir(), n)
		}
	}
	return files, nil
}

var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice)(\.(txt|md))?$`)

// goLicenses copies the licence files of every module fennec-cua links
// into dir and answers how many modules it recorded. A module without a
// licence file stops the build.
func goLicenses(ctx context.Context, arch, dir string) (int, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}", "./cmd/fennec-cua")
	cmd.Env = append(os.Environ(), buildEnv(arch)...)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]bool{}
	var index []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[0] == "fennec/cua" || seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		path, version, mdir := parts[0], parts[1], parts[2]
		entries, err := os.ReadDir(mdir)
		if err != nil {
			return 0, err
		}
		found := 0
		for _, e := range entries {
			if e.IsDir() || !licenseFile.MatchString(e.Name()) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(mdir, e.Name()))
			if err != nil {
				return 0, err
			}
			name := strings.ReplaceAll(path, "/", "_") + "@" + version + "-" + e.Name()
			if !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".md") {
				name += ".txt"
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return 0, err
			}
			found++
		}
		if found == 0 {
			return 0, fmt.Errorf("the Go module %s@%s has no licence file", path, version)
		}
		index = append(index, path+" "+version)
	}
	goroot, err := exec.CommandContext(ctx, "go", "env", "GOROOT").Output()
	if err != nil {
		return 0, fmt.Errorf("go env GOROOT: %w", err)
	}
	// Homebrew keeps it beside GOROOT (libexec) rather than in it.
	root := strings.TrimSpace(string(goroot))
	std, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		std, err = os.ReadFile(filepath.Join(filepath.Dir(root), "LICENSE"))
	}
	if err != nil {
		return 0, fmt.Errorf("the Go standard library's licence: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go-standard-library-LICENSE.txt"), std, 0o644); err != nil {
		return 0, err
	}
	index = append(index, "the Go standard library and runtime (go-standard-library-LICENSE.txt)")
	sort.Strings(index)
	text := "Go modules linked into fennec-cua, with their licence files beside this one:\n\n" + strings.Join(index, "\n") + "\n"
	return len(index), os.WriteFile(filepath.Join(dir, "README.txt"), []byte(text), 0o644)
}

// verifyApp checks the signature of the whole app and that its programs
// start under their signatures.
func verifyApp(ctx context.Context, app, version string, rel driver.Release) error {
	if err := macsign.VerifyDeep(app); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, standalone.Executable(app), "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != version {
		return fmt.Errorf("the signed fennec-cua answers version %q (%v); want %q", strings.TrimSpace(string(out)), err, version)
	}
	out, err = exec.CommandContext(ctx, filepath.Join(app, "Contents", "MacOS", "cua-driver"), "--version").CombinedOutput()
	base, _, _ := strings.Cut(rel.Version, "-")
	if err != nil || !strings.Contains(string(out), base) {
		return fmt.Errorf("the signed cua-driver answers %q (%v); want version %s", strings.TrimSpace(string(out)), err, base)
	}
	return nil
}

// notarize submits the app to Apple's notary service, staples the ticket
// and checks that Gatekeeper accepts it.
func notarize(ctx context.Context, app, profile, work string) error {
	zip := filepath.Join(work, "notarize.zip")
	steps := [][]string{
		{"/usr/bin/ditto", "-c", "-k", "--keepParent", app, zip},
		{"xcrun", "notarytool", "submit", zip, "--keychain-profile", profile, "--wait"},
		{"xcrun", "stapler", "staple", app},
		{"xcrun", "stapler", "validate", app},
		{"spctl", "--assess", "--type", "execute", "-vv", app},
	}
	for _, s := range steps {
		step("%s", strings.Join(s[:min(3, len(s))], " "))
		cmd := exec.CommandContext(ctx, s[0], s[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(s[:2], " "), err)
		}
	}
	return nil
}

func installText(version, top string, notarized, core bool) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s for macOS 13 or later on Apple silicon\n\n", standalone.DisplayName, version)
	if !notarized {
		fmt.Fprintf(&b, `This build is signed with a local certificate and is not notarized. If you downloaded the
archive with a browser, clear the download quarantine before running anything in it:

  xattr -dr com.apple.quarantine %s

`, top)
	}
	fmt.Fprintf(&b, `Install for this user (no administrator rights; copies the app to ~/Applications and links
~/.local/bin/fennec-cua):

  ./%s/Contents/MacOS/fennec-cua install

Then grant the permissions it prints (on macOS 15 and later, also choose Allow when
"fennec-cua permissions --request" makes macOS ask to capture the screen directly), close
any macOS prompt left on screen, and check everything with:

  fennec-cua doctor

Connect an agent:     fennec-cua mcp-config
Shell-only agents:    fennec-cua js 'await cua.listApps()'
Agent skill:          fennec-cua skill install --claude   (or --codex, or --dest DIR)
Uninstall:            fennec-cua uninstall [--purge]

Licences and third-party notices: %s/Contents/Resources/licenses/
`, standalone.AppName, standalone.AppName)
	if core {
		b.WriteString("\nThis is the core archive without the bundled Chromium: browser tabs are unavailable; native apps work.\n")
	}
	return b.Bytes()
}

// sourceDate is the archive entries' time: SOURCE_DATE_EPOCH, else the HEAD
// commit's time, else now.
func sourceDate() time.Time {
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0).UTC()
		}
	}
	if out, err := exec.Command("git", "log", "-1", "--format=%ct").Output(); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Now().UTC().Truncate(time.Second)
}

func gitDescribe() string {
	out, err := exec.Command("git", "describe", "--always", "--dirty", "--abbrev=12").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeTarGz writes root/top as a gzip-compressed tarball: entries sorted
// by path, directories, regular files and symbolic links only, owned by
// root, dated mtime, with their permission bits.
func writeTarGz(dst, root, top string, mtime time.Time) error {
	var paths []string
	err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		h := &tar.Header{Name: name, ModTime: mtime, Mode: int64(fi.Mode().Perm()), Format: tar.FormatPAX}
		switch {
		case fi.IsDir():
			h.Typeflag, h.Name = tar.TypeDir, name+"/"
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if filepath.IsAbs(target) {
				return fmt.Errorf("refusing the absolute link %s -> %s", p, target)
			}
			h.Typeflag, h.Linkname = tar.TypeSymlink, target
		case fi.Mode().IsRegular():
			h.Typeflag, h.Size = tar.TypeReg, fi.Size()
		default:
			return fmt.Errorf("refusing %s: not a file, directory or link", p)
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			if err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}
