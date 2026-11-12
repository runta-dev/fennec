//go:build darwin

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func isRosettaTranslated() bool {
	val, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && val == 1
}

func isReadOnlyFilesystem(path string) bool {
	var stat unix.Statfs_t
	return unix.Statfs(path, &stat) == nil && stat.Flags&unix.MNT_RDONLY != 0
}

func unpackArchive(ctx context.Context, zipPath, destDir string) error {
	out, err := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", zipPath, destDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("unpack archive: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unregisterLaunchServices(path string) {
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if info, err := os.Stat(lsregister); err == nil && !info.IsDir() {
		_ = exec.Command(lsregister, "-u", path).Run()
	}
}

// VerifySameSigner verifies strict code signature integrity and enforces that
// the downloaded bundle was signed by the exact same Apple TeamIdentifier as
// the currently installed bundle, or the exact same X.509 leaf certificate for
// local development identities without a TeamIdentifier. Ad-hoc signatures are
// always rejected.
func VerifySameSigner(ctx context.Context, installedBundle, candidateBundle string) error {
	if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", candidateBundle).CombinedOutput(); err != nil {
		return fmt.Errorf("downloaded signature verification failed: %s", strings.TrimSpace(string(out)))
	}
	wantTeam := bundleTeamIdentifier(ctx, installedBundle)
	gotTeam := bundleTeamIdentifier(ctx, candidateBundle)
	if wantTeam != "" || gotTeam != "" {
		if wantTeam == "" || wantTeam != gotTeam {
			return fmt.Errorf("downloaded bundle signer %q does not match installed signer %q", gotTeam, wantTeam)
		}
		return nil
	}
	wantCert := bundleLeafCertificateDigest(ctx, installedBundle)
	gotCert := bundleLeafCertificateDigest(ctx, candidateBundle)
	if wantCert == "" || wantCert != gotCert {
		return fmt.Errorf("downloaded bundle signer %q does not match installed signer %q", gotTeam, wantTeam)
	}
	return nil
}

func bundleTeamIdentifier(ctx context.Context, app string) string {
	out, _ := exec.CommandContext(ctx, "/usr/bin/codesign", "-dv", app).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if val, ok := strings.CutPrefix(strings.TrimSpace(line), "TeamIdentifier="); ok && val != "" && val != "not set" {
			return val
		}
	}
	return ""
}

func bundleLeafCertificateDigest(ctx context.Context, app string) string {
	dir, err := os.MkdirTemp("", "fennec-cert-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "cert")
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--extract-certificates="+prefix, app).Run(); err != nil {
		return ""
	}
	der, err := os.ReadFile(prefix + "0")
	if err != nil || len(der) == 0 {
		return ""
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
