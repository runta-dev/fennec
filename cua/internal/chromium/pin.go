// Package chromium runs the Chromium that fennec-cua bundles for agent
// browsing: open-source Chromium (BSD-3-Clause and its third-party
// notices), never Google Chrome or Chrome for Testing. It pins the build,
// locates the bundled app, launches it on demand with its own profile and
// a DevTools pipe, and supervises it.
package chromium

import (
	"fmt"
	"runtime"
)

// Build is one pinned Chromium snapshot from the public
// chromium-browser-snapshots bucket.
type Build struct {
	Arch     string // Go architecture
	Bucket   string // the bucket's platform folder
	Revision string // Chromium commit position on main
	Commit   string // chromium/src commit
	Version  string
	SHA256   string // of chrome-mac.zip
	Size     int64
	// CreditsSHA256 is the SHA-256 of the third-party notices page
	// (licenses.py credits rules, internal/credits) generated from
	// Commit and the dependencies its DEPS pins for macOS.
	CreditsSHA256 string
}

// URL is where the build's archive is downloaded from.
func (b Build) URL() string {
	return "https://storage.googleapis.com/chromium-browser-snapshots/" + b.Bucket + "/" + b.Revision + "/chrome-mac.zip"
}

// Pinned is the one pinned snapshot. Fennec ships for Apple silicon only,
// so there is no other architecture. Update the fields together (docs:
// README, "Bundled Chromium"); cua-package verifies the hash before
// anything else.
var Pinned = Build{
	Arch: "arm64", Bucket: "Mac_Arm", Revision: "1743750",
	Commit:        "2c8bee4ceb39a8f0ebf674b3327138b5e0ebea57",
	Version:       "160.0.8159.0",
	SHA256:        "14e94ce639bde8031f31ee9f0935bba892d6955fa36bab2eae3b7b43cbc45f18",
	Size:          184457146,
	CreditsSHA256: "085e2ec9c87be3a957497969189e3c8e44534ba622bf833b31617aec32e7d711",
}

// BuildFor returns the pinned build for a Go architecture ("" means this
// machine's). Only arm64 (Apple silicon) is pinned; any other architecture
// is an error.
func BuildFor(arch string) (Build, error) {
	if arch == "" {
		arch = runtime.GOARCH
	}
	if arch != Pinned.Arch {
		return Build{}, fmt.Errorf("the bundled Chromium is pinned for Apple silicon (arm64) only; this is %s", arch)
	}
	return Pinned, nil
}
