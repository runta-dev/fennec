//go:build darwin

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/installation"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifySameSignerRejectsAdHocAndAcceptsMatchingCertificate(t *testing.T) {
	root := t.TempDir()
	adhocA := createInstalledFixtureBundle(t, filepath.Join(root, "a"), "0.2.100")
	adhocB := createInstalledFixtureBundle(t, filepath.Join(root, "b"), "0.2.200")
	for _, app := range []string{adhocA, adhocB} {
		if out, err := exec.Command("/usr/bin/codesign", "--force", "--deep", "--sign", "-", app).CombinedOutput(); err != nil {
			t.Fatalf("ad-hoc codesign failed: %v (%s)", err, out)
		}
	}
	if err := VerifySameSigner(context.Background(), adhocA, adhocB); err == nil {
		t.Fatal("ad-hoc signature unexpectedly passed VerifySameSigner")
	}

	out, err := exec.Command("/usr/bin/security", "find-identity", "-v", "-p", "codesigning").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Fennec Local Development") {
		return
	}
	devA := createInstalledFixtureBundle(t, filepath.Join(root, "dev-a"), "0.2.100")
	devB := createInstalledFixtureBundle(t, filepath.Join(root, "dev-b"), "0.2.200")
	for _, app := range []string{devA, devB} {
		if signOut, signErr := exec.Command("/usr/bin/codesign", "--force", "--deep", "--sign", "Fennec Local Development", app).CombinedOutput(); signErr != nil {
			t.Fatalf("development codesign failed: %v (%s)", signErr, signOut)
		}
	}
	if err := VerifySameSigner(context.Background(), devA, devB); err != nil {
		t.Fatalf("matching Fennec Local Development certificate rejected: %v", err)
	}
	if err := VerifySameSigner(context.Background(), devA, adhocB); err == nil {
		t.Fatal("ad-hoc bundle accepted against certificate-signed bundle")
	}
	if err := os.WriteFile(filepath.Join(devB, "Contents/MacOS/fen"), []byte("tampered"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := VerifySameSigner(context.Background(), devA, devB); err == nil {
		t.Fatal("tampered bundle accepted by VerifySameSigner")
	}
}

func TestRealSignedBundleDittoStageAndTransactionalInstall(t *testing.T) {
	out, err := exec.Command("/usr/bin/security", "find-identity", "-v", "-p", "codesigning").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Fennec Local Development") {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := os.MkdirTemp(home, ".fennec-real-bundle-update-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sandbox)

	installedDir := filepath.Join(sandbox, "installed")
	installedApp := createInstalledFixtureBundle(t, installedDir, "0.2.11120001")
	if signOut, signErr := exec.Command("/usr/bin/codesign", "--force", "--deep", "--sign", "Fennec Local Development", installedApp).CombinedOutput(); signErr != nil {
		t.Fatalf("sign installed bundle: %v (%s)", signErr, signOut)
	}

	candidateDir := filepath.Join(sandbox, "candidate")
	candidateApp := createInstalledFixtureBundle(t, candidateDir, "0.2.11129999")
	if signOut, signErr := exec.Command("/usr/bin/codesign", "--force", "--deep", "--sign", "Fennec Local Development", candidateApp).CombinedOutput(); signErr != nil {
		t.Fatalf("sign candidate bundle: %v (%s)", signErr, signOut)
	}

	arch := runtime.GOARCH
	assetName := AppAssetName("0.2.11129999", arch)
	zipPath := filepath.Join(sandbox, assetName)
	if dittoOut, dittoErr := exec.Command("/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", candidateApp, zipPath).CombinedOutput(); dittoErr != nil {
		t.Fatalf("ditto zip failed: %v (%s)", dittoErr, dittoOut)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBytes)
	sumHex := hex.EncodeToString(sum[:])

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.11129999",
				"body":     "### Verified\n- Real ditto + codesign + atomic rename",
				"html_url": srvURL + "/releases/v0.2.11129999",
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": srvURL + "/" + assetName, "size": len(zipBytes), "digest": "sha256:" + sumHex},
				},
			})
		case "/" + assetName:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	svc := NewService(Config{
		Bundle:     installedApp,
		Feed:       func() string { return srv.URL + "/latest" },
		HTTPClient: srv.Client(),
		Transport:  srv.Client().Transport,
		ApplyUpdate: func(ctx context.Context, stagedApp, destinationBundle string) error {
			ops := installation.SystemOperations()
			ops.StopUI = nil
			ops.OpenUI = nil
			_, installErr := installation.Install(ctx, stagedApp, destinationBundle, true, ops)
			return installErr
		},
	})

	ctx := context.Background()
	snap := svc.Check(ctx, true)
	if snap.State != "ready" || snap.Latest != "0.2.11129999" {
		t.Fatalf("expected ready state after real ditto unpack + VerifySameSigner, got %+v", snap)
	}
	if _, err := svc.Install(ctx); err != nil {
		t.Fatalf("transactional installation.Install failed: %v", err)
	}
	if got := BundleVersion(installedApp); got != "0.2.11129999" {
		t.Fatalf("expected upgraded bundle version 0.2.11129999, got %s", got)
	}
	if verifyOut, verifyErr := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", installedApp).CombinedOutput(); verifyErr != nil {
		t.Fatalf("upgraded bundle failed codesign --verify: %v (%s)", verifyErr, verifyOut)
	}
}
