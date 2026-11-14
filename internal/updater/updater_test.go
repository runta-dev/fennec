package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVersionComparisonAndReleasedDetection(t *testing.T) {
	if !Newer("0.2.11121200", "0.2.11121023") {
		t.Fatal("expected newer timestamp build")
	}
	if Newer("0.2.11121023", "0.2.11121023") {
		t.Fatal("equal version treated as newer")
	}
	if !Newer("0.2.1", "0.2.1-rc.2") || !Newer("0.2.1-rc.10", "0.2.1-rc.9") {
		t.Fatal("prerelease ordering broken")
	}
	if Released("dev") || Released("0.2.0") || Released("0.2.1-3-gabcdef1") || Released("0.2.1-dirty") || Released("0.3.11141342-dev") {
		t.Fatal("development build treated as released")
	}
	if !Released("0.2.11121023") {
		t.Fatal("production version rejected")
	}
}

func createTestBundleZip(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runta.fennec</string><key>CFBundleShortVersionString</key><string>` + version + `</string></dict></plist>`
	w, err := zw.Create("Fennec.app/Contents/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(plist)); err != nil {
		t.Fatal(err)
	}
	hdr := &zip.FileHeader{Name: "Fennec.app/Contents/MacOS/fen", Method: zip.Deflate}
	hdr.SetMode(0755)
	bin, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	// Pad binary so Range resumption exercises multi-chunk transfers.
	payload := "#!/bin/sh\necho Fennec " + version + "\n#" + strings.Repeat("x", 32768) + "\n"
	if _, err := bin.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func createInstalledFixtureBundle(t *testing.T, dir, version string) string {
	t.Helper()
	app := filepath.Join(dir, "Fennec.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents/MacOS"), 0755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runta.fennec</string><key>CFBundleShortVersionString</key><string>` + version + `</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents/MacOS/fen"), []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestServiceResumesInterruptedDownloadVerifiesSHAAndInstalls(t *testing.T) {
	// Place the test bundle outside TempDir (/var/folders/.../T) because
	// RequiresInstallation treats os.TempDir() as an uninstalled staging path!
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := os.MkdirTemp(home, ".fennec-updater-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sandbox)

	installed := createInstalledFixtureBundle(t, sandbox, "0.2.100")
	zipBytes := createTestBundleZip(t, "0.2.200")
	sum := sha256.Sum256(zipBytes)
	sumHex := hex.EncodeToString(sum[:])
	assetName := AppAssetName("0.2.200", "arm64")

	var zipHits atomic.Int32
	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.200",
				"body":     "### Improvements\n- Faster cloud reconnect",
				"html_url": serverURL + "/release/v0.2.200",
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": serverURL + "/download/" + assetName, "size": len(zipBytes)},
					{"name": "SHA256SUMS", "browser_download_url": serverURL + "/download/SHA256SUMS", "size": 90},
				},
			})
		case "/download/SHA256SUMS":
			_, _ = fmt.Fprintf(w, "%s  %s\n", sumHex, assetName)
		case "/download/" + assetName:
			hit := zipHits.Add(1)
			if hit == 1 {
				// Drop connection mid-stream to verify HTTP Range resumption!
				half := len(zipBytes) / 2
				w.Header().Set("Content-Length", strconv.Itoa(len(zipBytes)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(zipBytes[:half])
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					if err == nil {
						conn.Close()
						return
					}
				}
				return
			}
			rangeHeader := r.Header.Get("Range")
			if strings.HasPrefix(rangeHeader, "bytes=") {
				offset, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rangeHeader, "bytes="), "-"))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(zipBytes)-1, len(zipBytes)))
				w.Header().Set("Content-Length", strconv.Itoa(len(zipBytes)-offset))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(zipBytes[offset:])
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL

	var signerChecked atomic.Bool
	var installedCalled atomic.Bool
	svc := NewService(Config{
		Bundle:     installed,
		Current:    "0.2.100",
		Arch:       "arm64",
		Feed:       func() string { return srv.URL + "/latest" },
		HTTPClient: srv.Client(),
		Transport:  srv.Client().Transport,
		VerifySigner: func(_ context.Context, oldApp, newApp string) error {
			if oldApp != installed || BundleVersion(newApp) != "0.2.200" {
				return fmt.Errorf("unexpected signer check args: %s -> %s", oldApp, newApp)
			}
			signerChecked.Store(true)
			return nil
		},
		ApplyUpdate: func(_ context.Context, stagedApp, dest string) error {
			if filepath.Ext(stagedApp) != ".app" || dest != installed {
				return fmt.Errorf("invalid apply paths: %s -> %s", stagedApp, dest)
			}
			installedCalled.Store(true)
			_ = os.RemoveAll(dest)
			return os.Rename(stagedApp, dest)
		},
	})

	ctx := context.Background()
	found := svc.Check(ctx, false)
	if found.State != "found" || found.Latest != "0.2.200" || found.Size != int64(len(zipBytes)) {
		t.Fatalf("expected found state before download, got %+v", found)
	}

	ready := svc.Download(ctx)
	if ready.State != "ready" || !signerChecked.Load() || zipHits.Load() < 2 {
		t.Fatalf("expected resumed ready download, got %+v (hits=%d signer=%v)", ready, zipHits.Load(), signerChecked.Load())
	}
	// Staged payload must NOT end in .app while waiting to avoid LaunchServices duplicate indexing.
	if _, err := os.Stat(filepath.Join(sandbox, ".fennec-autoupdate/staged-payload")); err != nil {
		t.Fatalf("expected non-.app staged-payload directory: %v", err)
	}

	after, err := svc.Install(ctx)
	if err != nil || !installedCalled.Load() {
		t.Fatalf("install failed: %v %+v", err, after)
	}
	if got := BundleVersion(installed); got != "0.2.200" {
		t.Fatalf("expected installed bundle to become 0.2.200, got %s", got)
	}
}

func TestTamperedArchiveRejectedBeforeUnpack(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := os.MkdirTemp(home, ".fennec-updater-tamper-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sandbox)

	installed := createInstalledFixtureBundle(t, sandbox, "0.2.100")
	zipBytes := createTestBundleZip(t, "0.2.200")
	assetName := AppAssetName("0.2.200", "arm64")
	wrongSum := strings.Repeat("a", 64)

	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "0.2.200",
				"url":     serverURL + "/release",
				"assets": map[string]any{
					assetName: map[string]any{"url": serverURL + "/zip", "size": len(zipBytes), "sha256": wrongSum},
				},
			})
			return
		}
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()
	serverURL = srv.URL

	svc := NewService(Config{
		Bundle:       installed,
		Current:      "0.2.100",
		Arch:         "arm64",
		Feed:         func() string { return srv.URL + "/latest" },
		HTTPClient:   srv.Client(),
		Transport:    srv.Client().Transport,
		VerifySigner: func(context.Context, string, string) error { return nil },
	})
	snap := svc.Check(context.Background(), true)
	if snap.State != "error" {
		t.Fatalf("expected checksum mismatch to fail with error state, got %+v", snap)
	}
}

// A failed check (a release mid-publish, a network blip) must not wait a
// whole CheckInterval: a fresh status read retries it once FreshAfter has
// passed, as it does for a "latest" state.
func TestFreshStatusRetriesFailedCheck(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.2.100", "assets": []map[string]any{}})
	}))
	defer srv.Close()
	s := NewService(Config{Current: "0.2.100", Feed: func() string { return srv.URL }, Arch: "arm64", HTTPClient: srv.Client(), AutoEnabled: func() bool { return true }})
	if snap := s.Check(context.Background(), false); snap.State != "error" || !snap.Retry {
		t.Fatalf("first check: %+v", snap)
	}
	s.mu.Lock()
	s.askedAt = time.Now().Add(-FreshAfter - time.Second)
	s.mu.Unlock()
	s.Snapshot(context.Background(), true)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snap := s.Snapshot(context.Background(), false); snap.State == "latest" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the failed check was not retried: %+v (feed hits %d)", s.Snapshot(context.Background(), false), hits.Load())
}
