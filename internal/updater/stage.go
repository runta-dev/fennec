package updater

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fennec/internal/installation"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".fennec-writable-*")
	if err != nil {
		return false
	}
	f.Close()
	_ = os.Remove(f.Name())
	return true
}

// StuckReason reports why bundle cannot replace itself in place, or "".
func StuckReason(bundle string) string {
	if bundle == "" {
		return "not-bundle"
	}
	if isReadOnlyFilesystem(bundle) {
		return "read-only"
	}
	if installation.RequiresInstallation(bundle, os.TempDir(), false) {
		return "uninstalled-location"
	}
	if filepath.Base(filepath.Dir(bundle)) == "build" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(bundle)), "go.mod")); err == nil {
			return "development-build"
		}
	}
	if !dirWritable(filepath.Dir(bundle)) {
		return "not-writable"
	}
	return ""
}

// CleanOldStaging removes leftover staging directories from earlier runs without
// touching active .fennec-update-* backup directories mapped by CLI processes.
func CleanOldStaging(bundle string) {
	if bundle == "" {
		return
	}
	_ = os.RemoveAll(stagingRoot(bundle))
}

func stagingRoot(bundle string) string {
	return filepath.Join(filepath.Dir(bundle), ".fennec-autoupdate")
}

// StageBundle downloads, verifies, unpacks, and signature-checks the release's
// macOS archive beside bundle. Like Magpie (#3965), the staged folder does not
// end in .app while waiting so LaunchServices/Spotlight never index a duplicate.
func StageBundle(ctx context.Context, transport http.RoundTripper, rel *Release, bundle string, arch string, opts progressOptions, verifySigner func(ctx context.Context, installedBundle, candidateBundle string) error) (string, error) {
	if arch == "" {
		arch = TargetArch()
	}
	assetName := AppAssetName(rel.Version, arch)
	asset, ok := rel.Assets[assetName]
	if !ok {
		return "", fmt.Errorf("release %s has no %s asset", rel.Version, assetName)
	}
	root := stagingRoot(bundle)
	_ = os.RemoveAll(root)
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(root, assetName)
	if err := downloadVerifiedAsset(ctx, transport, asset, zipPath, opts); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	unpackDir := filepath.Join(root, "unpack")
	if opts.onStep != nil {
		opts.onStep(StepUnpack)
	}
	if err := unpackArchive(ctx, zipPath, unpackDir); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	_ = os.Remove(zipPath)

	unzipped := filepath.Join(unpackDir, "Fennec.app")
	staged := filepath.Join(root, "staged-payload")
	if err := os.Rename(unzipped, staged); err != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("stage unpacked bundle: %w", err)
	}
	_ = os.RemoveAll(unpackDir)
	unregisterLaunchServices(unzipped)

	if opts.onStep != nil {
		opts.onStep(StepSignature)
	}
	if id, err := stagedBundleIdentifier(staged); err != nil || id != "com.runta.fennec" {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("downloaded bundle has unexpected identifier")
	}
	if verifySigner == nil {
		verifySigner = VerifySameSigner
	}
	if err := verifySigner(ctx, bundle, staged); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	return staged, nil
}

func stagedBundleIdentifier(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("staged bundle directory required")
	}
	file, err := os.Open(filepath.Join(dir, "Contents/Info.plist"))
	if err != nil {
		return "", err
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, 1<<20))
	want := false
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return "", err
			}
			want = key == "CFBundleIdentifier"
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return "", err
			}
			if want {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("bundle identifier missing")
}

func unpackZipPortable(zipPath, destDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	cleanDest := filepath.Clean(destDir)
	if err := os.MkdirAll(cleanDest, 0755); err != nil {
		return err
	}
	for _, f := range reader.File {
		target := filepath.Join(cleanDest, f.Name)
		rel, err := filepath.Rel(cleanDest, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive entry escapes destination: %s", f.Name)
		}
		mode := f.Mode()
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			linkTarget, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if readErr != nil {
				return readErr
			}
			if err := os.Symlink(string(linkTarget), target); err != nil {
				return err
			}
			continue
		}
		perm := mode.Perm()
		if perm == 0 {
			perm = 0644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, 256<<20))
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
