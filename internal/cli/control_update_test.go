package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/updater"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func makeUpdateTestArchive(t *testing.T, version string) []byte {
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
	if _, err := bin.Write([]byte("#!/bin/sh\necho " + version + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFenUpdateCommandsAndAutoUpdateSettingsEndToEnd(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := os.MkdirTemp(userHome, ".fennec-cli-update-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sandbox)

	ipcHome, err := os.MkdirTemp("/tmp", "fen-upd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ipcHome)
	t.Setenv("HOME", ipcHome)

	appDir := filepath.Join(sandbox, "Fennec.app")
	if err := os.MkdirAll(filepath.Join(appDir, "Contents/MacOS"), 0755); err != nil {
		t.Fatal(err)
	}
	initialPlist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runta.fennec</string><key>CFBundleShortVersionString</key><string>0.2.100</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(appDir, "Contents/Info.plist"), []byte(initialPlist), 0644); err != nil {
		t.Fatal(err)
	}
	fenExec := filepath.Join(appDir, "Contents/MacOS/fen")
	if err := os.WriteFile(fenExec, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	zipBytes := makeUpdateTestArchive(t, "0.2.250")
	sum := sha256.Sum256(zipBytes)
	sumHex := hex.EncodeToString(sum[:])
	arch := runtime.GOARCH
	assetName := updater.AppAssetName("0.2.250", arch)

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.250",
				"body":     "### Features\n- End-to-end updater validation",
				"html_url": srvURL + "/releases/v0.2.250",
				"assets": []map[string]any{
					{
						"name":                 assetName,
						"browser_download_url": srvURL + "/assets/" + assetName,
						"size":                 len(zipBytes),
						"digest":               "sha256:" + sumHex,
					},
				},
			})
		case "/assets/" + assetName:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	t.Setenv("FENNEC_UPDATE_FEED", srv.URL+"/latest")

	settings := newDesktopSettings(ipcHome)
	network := connectivity.NewMonitor(func() bool { return true }, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := &controlplane.Controller{
		Account:      account.New(ctx, serviceCredentials{}, ""),
		Connectivity: network,
		Settings:     settings.snapshot,
		Additional:   map[string]control.Handler{},
	}
	controller.Additional["settings.get"] = func(context.Context, json.RawMessage) (any, error) { return settings.snapshot(), nil }
	controller.Additional["settings.set"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		return settings.update(ctx, raw, nil)
	}

	var applied atomic.Int32
	svc := updater.NewService(updater.Config{
		Bundle:     appDir,
		Current:    "0.2.100",
		Arch:       arch,
		HTTPClient: srv.Client(),
		Transport:  srv.Client().Transport,
		Network:    network,
		AutoEnabled: func() bool {
			prefs, ok := settings.snapshot().(desktopPreferences)
			return !ok || !prefs.NoAutoUpdate
		},
		VerifySigner: func(context.Context, string, string) error { return nil },
		ApplyUpdate: func(_ context.Context, stagedApp, dest string) error {
			applied.Add(1)
			_ = os.RemoveAll(dest)
			return os.Rename(stagedApp, dest)
		},
	})
	controller.UpdateState = func() any { return svc.Snapshot(context.Background(), false) }
	controller.Additional["update.status"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Snapshot(ctx, true), nil
	}
	controller.Additional["update.check"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Check(ctx, true), nil
	}
	controller.Additional["update.download"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Download(ctx), nil
	}
	controller.Additional["update.install"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Install(ctx)
	}

	socket := filepath.Join(ipcHome, ".config/fennec/ipc/control.sock")
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- control.Serve(ctx, socket, controller.Handlers(), func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("control socket start timeout")
	}

	var out bytes.Buffer
	if err := fenUpdateCommand([]string{"auto", "off"}, &out); err != nil {
		t.Fatalf("fen update auto off failed: %v", err)
	}
	if !strings.Contains(out.String(), `"auto_update":false`) {
		t.Fatalf("unexpected auto off output: %s", out.String())
	}

	out.Reset()
	handled, err := fenControlCommand([]string{"settings", "auto-update", "on"}, &out)
	if !handled || err != nil {
		t.Fatalf("fen settings auto-update on failed: handled=%v err=%v", handled, err)
	}

	out.Reset()
	if err := fenUpdateCommand([]string{"check"}, &out); err != nil {
		t.Fatalf("fen update check failed: %v", err)
	}
	var snap updater.Snapshot
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.State != "ready" || snap.Latest != "0.2.250" {
		t.Fatalf("expected ready snapshot for 0.2.250, got %+v", snap)
	}

	out.Reset()
	if err := fenUpdateCommand(nil, &out); err != nil {
		t.Fatalf("fen update install failed: %v", err)
	}
	if applied.Load() != 1 || updater.BundleVersion(appDir) != "0.2.250" {
		t.Fatalf("expected bundle upgraded to 0.2.250, applied=%d version=%s", applied.Load(), updater.BundleVersion(appDir))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
