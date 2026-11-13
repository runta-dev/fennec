package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/klauspost/compress/zstd"
)

// Terminal service upgrades travel over the cloud SSH gateway, which can be
// slow. Consecutive builds share most of their bytes, so the upgrade is sent
// as a zstd frame that uses the binary already on the cloud computer as its
// dictionary: roughly 0.4 MB instead of 4 MB. The installed result is always
// verified against the new binary's SHA-256 before it replaces anything.

const (
	terminalPatchMaxOutput = 64 << 20
	terminalPatchWindow    = 16 << 20
)

var terminalVersionPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// terminalCacheDir holds the last binary uploaded per architecture, the
// dictionary for the next upgrade. Tests override it.
var terminalCacheDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "fennec", "terminal-cache")
}

// EncodeTerminalPatch returns new encoded against old.
func EncodeTerminalPatch(old, new []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(terminalPatchWindow), zstd.WithEncoderDictRaw(0, old))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(new, nil), nil
}

// ApplyTerminalPatch reads a patch from r, decodes it against the binary at
// oldPath and writes the result to w.
func ApplyTerminalPatch(oldPath string, r io.Reader, w io.Writer) error {
	old, err := os.ReadFile(oldPath)
	if err != nil {
		return err
	}
	patch, err := io.ReadAll(io.LimitReader(r, terminalPatchMaxOutput))
	if err != nil {
		return err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDictRaw(0, old), zstd.WithDecoderMaxWindow(terminalPatchWindow), zstd.WithDecoderMaxMemory(terminalPatchMaxOutput))
	if err != nil {
		return err
	}
	defer dec.Close()
	out, err := dec.DecodeAll(patch, nil)
	if err != nil {
		return fmt.Errorf("invalid terminal patch: %w", err)
	}
	_, err = w.Write(out)
	return err
}

// cachedTerminalBinary returns the cached binary for arch when its digest is
// version.
func cachedTerminalBinary(arch, version string) []byte {
	dir := terminalCacheDir()
	if dir == "" || !terminalVersionPattern.MatchString(version) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "fennec-terminal-linux-"+arch))
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != version {
		return nil
	}
	return data
}

// rememberTerminalBinary records the binary now installed on the cloud
// computer. Failure only costs the next upgrade a full upload.
func rememberTerminalBinary(arch string, data []byte) {
	dir := terminalCacheDir()
	if dir == "" || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	path := filepath.Join(dir, "fennec-terminal-linux-"+arch)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		os.Remove(tmp)
		return
	}
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
}
