package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type preparationCheckpoint struct {
	Fingerprint              string              `json:"fingerprint"`
	ConfigurationFingerprint string              `json:"configuration_fingerprint,omitempty"`
	GatewayFingerprint       string              `json:"gateway_fingerprint,omitempty"`
	Events                   []map[string]string `json:"events"`
}

// Bump when local preparation orchestration changes the meaning of a successful
// checkpoint. Remote implementations and setup scripts are hashed separately;
// unrelated UI, diagnostics, signing, and local daemon edits must not reinstall
// an already prepared cloud environment on every application update.
const preparationSchema = "fennec-preparation-v2"

// A credential or model change does not change an installed gateway. Keep its
// installation/service rules and authenticated scope independent of account data.
func gatewayPreparationFingerprint(token, runtimeID string) string {
	data, _ := json.Marshal([]string{"fennec-gateway-v1", token, runtimeID, setup.MagpieSetupScript, setup.MagpieServiceScript})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func checkedServiceCommand(check, service string) string {
	script := "set -e\n" + check + "\nexec " + service + "\n"
	return "sh -c '" + strings.ReplaceAll(script, "'", "'\"'\"'") + "'"
}

func checkpointTerminalCommand(fingerprint string, agents []string) string {
	return checkpointTerminalCommandForMarker(fingerprint, agents, "prepared-runtime")
}

func checkpointTerminalCommandForMarker(fingerprint string, agents []string, marker string, magpie ...bool) string {
	return checkedServiceCommand(checkpointHealthScriptForMarker(fingerprint, agents, marker, magpie...), fmt.Sprintf(`"$HOME/.local/fennec/bin/fennec-terminal" serve --protocol=%d`, terminalstream.ProtocolVersion))
}

// Contains only digests and public preparation results, never credential contents.
func preparationFingerprint(home, token, runtimeID string, inputs any, artifacts string) (string, error) {
	return scopedPreparationFingerprint(home, token, runtimeID, inputs, artifacts, true)
}

func installationFingerprint(home, token, runtimeID string, inputs any, artifacts string) (string, error) {
	return scopedPreparationFingerprint(home, token, runtimeID, inputs, artifacts, false)
}

func scopedPreparationFingerprint(home, token, runtimeID string, inputs any, artifacts string, credentials bool) (string, error) {
	full, _, err := preparationFingerprints(home, token, runtimeID, inputs, artifacts, credentials)
	return full, err
}

// Configuration retains identity, credentials, routes, schema and setup-script
// scope. A new terminal executable alone requires installation, not re-syncing
// unchanged credentials. Both digests use the same input snapshot.
func preparationFingerprints(home, token, runtimeID string, inputs any, artifacts string, credentials bool) (full, configuration string, err error) {
	h := sha256.New()
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", "", err
	}
	kind := "fennec-install-v1"
	var files []string
	if credentials {
		kind = "fennec-prepare-v1"
		files = []string{"providers.json", "logins.json", "plugin-auth.json", "migrations.json", "plugin-providers.json"}
	}
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", kind, token, runtimeID, data)
	fmt.Fprintf(h, "\x00%s", preparationSchema)
	for _, script := range []string{setup.GuestCapacityScript, setup.HerdrSetupScript, setup.NodeSetupScript, setup.MagpieSetupScript, setup.MagpieServiceScript} {
		fmt.Fprintf(h, "\x00%x", sha256.Sum256([]byte(script)))
	}
	for _, name := range files {
		path := filepath.Join(home, ".config", "magpie", name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "\x00%s:missing", name)
			continue
		}
		if err != nil {
			return "", "", err
		}
		fmt.Fprintf(h, "\x00%s:%x", name, sha256.Sum256(preparationCredentialContent(name, data)))
	}
	configuration = hex.EncodeToString(h.Sum(nil))
	for _, name := range []string{"fennec-terminal-linux-amd64", "fennec-terminal-linux-arm64"} {
		file, err := os.Open(filepath.Join(artifacts, name))
		if err != nil {
			return "", "", err
		}
		sum := sha256.New()
		_, err = io.Copy(sum, file)
		file.Close()
		if err != nil {
			return "", "", err
		}
		fmt.Fprintf(h, "\x00%s:%x", name, sum.Sum(nil))
	}
	return hex.EncodeToString(h.Sum(nil)), configuration, nil
}

// Magpie's last-observed timestamp is not an authentication or routing input.
// Preserve every other field, including unknown fields and all nested auth data.
// Canonical JSON also prevents whitespace/key order from invalidating setup.
func preparationCredentialContent(name string, data []byte) []byte {
	value := canonicalPreparationFile(name, data)
	if name == "logins.json" {
		if entries, ok := value.([]any); ok {
			for _, value := range entries {
				if entry, ok := value.(map[string]any); ok {
					delete(entry, "seen")
				}
			}
		}
	}
	if canonical, err := json.Marshal(value); err == nil {
		return canonical
	}
	return data
}
func checkpointPath(home string) string {
	return filepath.Join(home, ".config", "fennec", "prepared-runtime.json")
}
func readPreparationCheckpoint(home, fingerprint string) (preparationCheckpoint, bool) {
	checkpoint, ok := loadPreparationCheckpoint(home)
	return checkpoint, ok && fingerprint != "" && checkpoint.Fingerprint == fingerprint
}

func loadPreparationCheckpoint(home string) (preparationCheckpoint, bool) {
	var checkpoint preparationCheckpoint
	data, err := os.ReadFile(checkpointPath(home))
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, &checkpoint) != nil || checkpoint.Fingerprint == "" || len(checkpoint.Events) == 0 {
		return preparationCheckpoint{}, false
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
	return checkpointHealthScriptForMarker(fingerprint, agents, "prepared-runtime")
}

func checkpointHealthScriptForMarker(fingerprint string, agents []string, marker string, magpie ...bool) string {
	return (setup.PreparationHealth{Fingerprint: fingerprint, Agents: agents, Marker: marker, Magpie: len(magpie) == 0 || magpie[0]}).Script()
}
