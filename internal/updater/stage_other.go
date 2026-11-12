//go:build !darwin

package updater

import (
	"context"
	"fmt"
)

func isRosettaTranslated() bool { return false }

func isReadOnlyFilesystem(_ string) bool { return false }

func unpackArchive(_ context.Context, zipPath, destDir string) error {
	return unpackZipPortable(zipPath, destDir)
}

func unregisterLaunchServices(_ string) {}

func VerifySameSigner(_ context.Context, _, _ string) error {
	return fmt.Errorf("macOS signature verification requires darwin")
}
