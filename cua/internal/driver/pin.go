package driver

import "fmt"

// Release is a pinned cua-driver release archive.
type Release struct {
	Version string
	Arch    string
	URL     string
	// SHA256 and Size are the archive's; the download is refused unless
	// both match.
	SHA256 string
	Size   int64
	// Files are the archive members the distribution ships, by name
	// inside the archive's top directory.
	Files []string
}

// Pinned is the cua-driver fennec-cua ships: the loomhand/cua fork (MIT,
// the only build with verify_hit), the version cmd/cua-bench and the live
// tests run against. Apple silicon only, like the rest of the
// distribution. Bump it deliberately: download the new release, record its
// size and SHA-256, rerun the live tests.
var Pinned = map[string]Release{
	"arm64": {
		Version: "0.47.14-loomhand.1",
		Arch:    "arm64",
		URL:     "https://github.com/loomhand/cua/releases/download/v0.47.14-loomhand.1/cua-driver-v0.47.14-loomhand.1-macos-arm64.tar.gz",
		SHA256:  "0e7d9050ee2130771ccf8e90324e5528aca468d39482cd3293733334aa8fb2e0",
		Size:    12087538,
		Files:   []string{"cua-driver", "cua-cursor-theme", "LICENSE", "THIRD_PARTY_NOTICES.md"},
	},
}

// PinnedFor answers the pin for an architecture (GOARCH).
func PinnedFor(arch string) (Release, error) {
	r, ok := Pinned[arch]
	if !ok {
		return Release{}, fmt.Errorf("no cua-driver is pinned for %s; the standalone distribution is built for Apple silicon (arm64) only", arch)
	}
	return r, nil
}

// TopDir is the archive's top directory.
func (r Release) TopDir() string {
	return "cua-driver-v" + r.Version + "-macos-" + r.Arch
}
