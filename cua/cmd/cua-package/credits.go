package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/chromium"
	"fennec/cua/internal/credits"
)

// creditsFiles are the generated notices of a build.
type creditsFiles struct {
	HTML, Text []byte
	Components int
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// generate builds the notices of a commit from its sources.
func generate(ctx context.Context, b chromium.Build, cache string) (*credits.Output, error) {
	f := &credits.Fetcher{Cache: cache, Log: func(format string, args ...any) { step(format, args...) }}
	tree, err := f.Tree(ctx, b.Commit, credits.MacPlatform(b.Arch))
	if err != nil {
		return nil, err
	}
	return credits.Generate(tree)
}

// buildCredits answers the build's notices: the cached ones when their
// hash is the pinned one, otherwise generated from the pinned commit's
// sources and checked against the pin.
func buildCredits(ctx context.Context, b chromium.Build, cache string) (*creditsFiles, error) {
	if b.CreditsSHA256 == "" {
		return nil, fmt.Errorf("no credits hash is pinned for %s; run cua-package -credits-out DIR -arch %s and pin the hash it prints", b.Arch, b.Arch)
	}
	dir := filepath.Join(cache, "out", b.Commit)
	if html, err := os.ReadFile(filepath.Join(dir, "Chromium-credits.html")); err == nil && sha256Hex(html) == b.CreditsSHA256 {
		text, err1 := os.ReadFile(filepath.Join(dir, "Chromium-credits.txt"))
		n, err2 := os.ReadFile(filepath.Join(dir, "components"))
		comps, err3 := strconv.Atoi(strings.TrimSpace(string(n)))
		if errors.Join(err1, err2, err3) == nil {
			return &creditsFiles{HTML: html, Text: text, Components: comps}, nil
		}
	}
	step("generating the third-party notices of %s from its sources (cache %s)", b.Commit[:12], cache)
	start := time.Now()
	out, err := generate(ctx, b, cache)
	if err != nil {
		return nil, fmt.Errorf("generating the credits: %w", err)
	}
	if got := out.SHA256(); got != b.CreditsSHA256 {
		return nil, fmt.Errorf("the generated credits have SHA-256 %s; the pin says %s (a changed source or generator: review, then pin the new hash)", got, b.CreditsSHA256)
	}
	step("generated %d components (%d bytes) in %v", out.Components, len(out.HTML), time.Since(start).Round(time.Second))
	if err := writeCreditsOut(dir, out); err != nil {
		return nil, err
	}
	return &creditsFiles{HTML: out.HTML, Text: out.Text, Components: out.Components}, nil
}

func writeCreditsOut(dir string, out *credits.Output) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"Chromium-credits.html": out.HTML,
		"Chromium-credits.txt":  out.Text,
		"components":            []byte(strconv.Itoa(out.Components) + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// creditsOnly is -credits-out: generate, write and describe, for a pin
// bump.
func creditsOnly(ctx context.Context, b chromium.Build, cache, outDir string) error {
	start := time.Now()
	out, err := generate(ctx, b, cache)
	if err != nil {
		return err
	}
	if err := writeCreditsOut(outDir, out); err != nil {
		return err
	}
	var skipped []string
	for _, p := range out.Scan.Skipped {
		skipped = append(skipped, p.Dir+": "+p.Message)
	}
	_ = os.WriteFile(filepath.Join(outDir, "skipped.txt"), []byte(strings.Join(skipped, "\n")+"\n"), 0o644)
	fmt.Printf("commit %s (%s)\ncomponents %d (%d shipped metadata entries in %d third-party directories; %d left out, see skipped.txt)\nhtml %d bytes, text %d bytes\nsha256 %s\ntime %v\n",
		b.Commit, b.Arch, out.Components, len(out.Scan.Credited), len(out.Scan.Dirs), len(out.Scan.Skipped), len(out.HTML), len(out.Text), out.SHA256(), time.Since(start).Round(time.Second))
	return nil
}

// fetchSource reads a file of chromium/src at a commit.
func fetchSource(ctx context.Context, commit, path string, limit int64) ([]byte, error) {
	u := credits.ChromiumSrc + "/+/" + commit + "/" + path + "?format=TEXT"
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
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
}

// frameworkDir is the versioned framework directory of an app.
func frameworkDir(app string) (string, error) {
	m, err := filepath.Glob(filepath.Join(app, "Contents", "Frameworks", "Chromium Framework.framework", "Versions", "*", "Chromium Framework"))
	if err != nil {
		return "", err
	}
	var real []string
	for _, p := range m {
		if fi, err := os.Lstat(filepath.Dir(p)); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			real = append(real, filepath.Dir(p))
		}
	}
	if len(real) != 1 {
		return "", fmt.Errorf("expected one framework version in %s, found %v", app, real)
	}
	return real[0], nil
}

// patchCreditsPage replaces the build's placeholder chrome://credits
// resource (sample_credits.html of the commit) with the generated page,
// stored uncompressed (ResourceBundle reads raw resources as they are).
func patchCreditsPage(ctx context.Context, app, commit string, page []byte) (uint16, error) {
	sample, err := fetchSource(ctx, commit, "components/resources/sample_credits.html", 1<<20)
	if err != nil {
		return 0, err
	}
	fw, err := frameworkDir(app)
	if err != nil {
		return 0, err
	}
	pakPath := filepath.Join(fw, "Resources", "resources.pak")
	data, err := os.ReadFile(pakPath)
	if err != nil {
		return 0, err
	}
	out, id, err := replaceResource(data, sample, page)
	if err != nil {
		return 0, fmt.Errorf("resources.pak: %w", err)
	}
	return id, os.WriteFile(pakPath, out, 0o644)
}

// patchSafeStorage renames the Keychain item Chromium keeps its cookie
// key in: the service-name constant (a NUL-terminated char array in the
// framework, found exactly once and at the start of a string) becomes
// "Fennec Safe Storage", padded with NULs to the same size (Chromium
// takes its length with strlen; checkSafeStorage proves it on the signed
// build). The marker tells fennec-cua the build is renamed.
func patchSafeStorage(app string) error {
	fw, err := frameworkDir(app)
	if err != nil {
		return err
	}
	bin := filepath.Join(fw, "Chromium Framework")
	data, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	old := []byte(chromium.UpstreamSafeStorageService + "\x00")
	repl := []byte(chromium.SafeStorageService)
	if len(repl) >= len(old) {
		return errors.New("the new Safe Storage name is longer than the old one")
	}
	repl = append(repl, make([]byte, len(old)-len(repl))...)
	i := bytes.Index(data, old)
	switch {
	case i < 0:
		return fmt.Errorf("%q is not in the framework", chromium.UpstreamSafeStorageService)
	case bytes.Contains(data[i+1:], old):
		return fmt.Errorf("%q occurs more than once in the framework", chromium.UpstreamSafeStorageService)
	case i == 0 || data[i-1] != 0:
		return fmt.Errorf("%q is not a string of its own in the framework", chromium.UpstreamSafeStorageService)
	}
	copy(data[i:], repl)
	fi, err := os.Stat(bin)
	if err != nil {
		return err
	}
	if err := os.WriteFile(bin, data, fi.Mode().Perm()); err != nil {
		return err
	}
	marker, _ := json.Marshal(chromium.SafeStorage{Service: chromium.SafeStorageService, Account: chromium.SafeStorageAccount})
	return os.WriteFile(filepath.Join(app, "Contents", "Resources", chromium.SafeStorageMarker), append(marker, '\n'), 0o644)
}

// checkSafeStorage starts the signed browser headless against a
// throwaway keychain (a temporary HOME; the user's Keychain is never
// used) and checks it keeps its key in a "Fennec Safe Storage" item and
// no other.
func checkSafeStorage(ctx context.Context, bin string) error {
	tmp, err := os.MkdirTemp("", "cua-package-keychain-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	k, err := chromium.NewTempKeychain(filepath.Join(tmp, "home"))
	if err != nil {
		return err
	}
	defer k.Close()
	b := chromium.New(chromium.Options{Bin: bin, ProfileDir: filepath.Join(tmp, "profile"), Headless: true,
		SafeStorageService: chromium.SafeStorageService, Env: k.Env, IdleTimeout: -1})
	c, _, err := b.Conn(ctx)
	if err != nil {
		b.Close()
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = c.Call(cctx, "", "Storage.setCookies", map[string]any{"cookies": []map[string]any{{
		"name": "probe", "value": "1", "domain": "example.test", "path": "/", "expires": float64(time.Now().Add(time.Hour).Unix())}}})
	cancel()
	b.Close()
	if err != nil {
		return err
	}
	s, err := k.Services()
	if err != nil {
		return err
	}
	if len(s) != 1 || s[0] != chromium.SafeStorageService {
		return fmt.Errorf("the browser's Keychain items are %q; want only %q", s, chromium.SafeStorageService)
	}
	return nil
}
