package cli

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

func installationCheckpointPath(home string) string {
	return filepath.Join(home, ".config/fennec/installed-runtime.json")
}

func installationCheckpointMatches(home, fingerprint string) bool {
	var checkpoint struct {
		Fingerprint string `json:"fingerprint"`
	}
	data, err := os.ReadFile(installationCheckpointPath(home))
	return err == nil && len(data) < 8192 && fingerprint != "" && json.Unmarshal(data, &checkpoint) == nil && checkpoint.Fingerprint == fingerprint
}

func installationMarkerScript(fingerprint string) string {
	if len(fingerprint) != 64 {
		return "exit 1"
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return "exit 1"
	}
	return `set -eu
umask 077
mkdir -p "$HOME/.config/fennec"
printf '%s' '` + fingerprint + `' > "$HOME/.config/fennec/prepared-installation.tmp"
mv "$HOME/.config/fennec/prepared-installation.tmp" "$HOME/.config/fennec/prepared-installation"
`
}
