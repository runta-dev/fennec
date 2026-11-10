package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type preparationCheckpoint struct {
	Fingerprint string              `json:"fingerprint"`
	Events      []map[string]string `json:"events"`
}

// Contains only digests and public preparation results, never credential contents.
func preparationFingerprint(home, token, runtimeID string, inputs any, artifacts string) (string, error) {
	h := sha256.New()
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "fennec-prepare-v1\x00%s\x00%s\x00%s", token, runtimeID, data)
	for _, name := range []string{"providers.json", "logins.json", "plugin-auth.json"} {
		path := filepath.Join(home, ".config", "magpie", name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "\x00%s:missing", name)
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "\x00%s:%x", name, sha256.Sum256(data))
	}
	for _, name := range []string{"fennec-terminal-linux-amd64", "fennec-terminal-linux-arm64"} {
		file, err := os.Open(filepath.Join(artifacts, name))
		if err != nil {
			return "", err
		}
		sum := sha256.New()
		_, err = io.Copy(sum, file)
		file.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "\x00%s:%x", name, sum.Sum(nil))
	}
	// A new daemon may contain changed installer/configuration semantics.
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func checkpointPath(home string) string {
	return filepath.Join(home, ".config", "fennec", "prepared-runtime.json")
}
func readPreparationCheckpoint(home, fingerprint string) (preparationCheckpoint, bool) {
	var checkpoint preparationCheckpoint
	data, err := os.ReadFile(checkpointPath(home))
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, &checkpoint) != nil || fingerprint == "" || checkpoint.Fingerprint != fingerprint || len(checkpoint.Events) == 0 {
		return checkpoint, false
	}
	return checkpoint, true
}
func savePreparationCheckpoint(home string, checkpoint preparationCheckpoint) error {
	directory := filepath.Dir(checkpointPath(home))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".prepared-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(checkpoint)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), checkpointPath(home))
}
func checkpointHealthScript(fingerprint string, agents []string) string {
	// Fingerprints and agent names are constrained here before entering shell source.
	if len(fingerprint) != 64 {
		return "exit 1"
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return "exit 1"
	}
	script := `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
test "$(cat "$HOME/.config/fennec/prepared-runtime")" = '` + fingerprint + `'
command -v node >/dev/null
command -v magpie >/dev/null
test -x "$HOME/.local/fennec/bin/fennec-terminal"
herdr-core server status >/dev/null 2>&1
curl --fail --silent --max-time 2 http://127.0.0.1:3425/v1/models >/dev/null
`
	for _, id := range agents {
		if !strings.Contains("|claude|codex|pi|opencode|", "|"+id+"|") || id == "" {
			return "exit 1"
		}
		script += "command -v " + id + " >/dev/null\n"
	}
	return script
}
