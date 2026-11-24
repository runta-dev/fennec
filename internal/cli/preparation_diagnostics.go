package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fennec/internal/setup"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Private diagnostics retain only hashes. Public cache status names changed
// inputs, never their values. They have no effect on readiness or authorization.
func recordPreparationInputs(home, token, runtimeID string, magpie bool, nativeAgents []string, versions []setup.Agent, routes []setup.AgentRoute, plugins []setup.ModelPlugin, native setup.ModelAccountsRequest) ([]string, bool) {
	digests := map[string]string{}
	put := func(name string, value any) {
		data, err := json.Marshal(value)
		if err == nil {
			digests[name] = fmt.Sprintf("%x", sha256.Sum256(data))
		}
	}
	put("account", token)
	put("runtime", runtimeID)
	put("magpie_enabled", magpie)
	if len(nativeAgents) > 0 {
		put("magpie_off_agents", nativeAgents)
	}
	put("agent_inventory", versions)
	put("routes", routes)
	put("plugins", plugins)
	put("native_problems", native.NativeProblems)
	put("setup_rules", []string{preparationSchema, setup.GuestCapacityScript, setup.HerdrSetupScript, setup.NodeSetupScript, setup.MagpieSetupScript, setup.MagpieServiceScript})
	for name, encoded := range native.Files {
		put("native/"+name, encoded)
		if data, err := base64.StdEncoding.DecodeString(encoded); err == nil {
			put("native_semantic/"+name, canonicalPreparationFile(name, data))
		}
	}
	if magpie {
		for _, name := range []string{"providers.json", "logins.json", "plugin-auth.json", "migrations.json", "plugin-providers.json"} {
			data, err := os.ReadFile(filepath.Join(home, ".config/magpie", name))
			if err != nil {
				put("magpie/"+name, "unavailable")
				continue
			}
			put("magpie/"+name, data)
			put("magpie_semantic/"+name, canonicalPreparationFile(name, data))
			put("magpie_effective/"+name, preparationCredentialContent(name, data))
		}
	}
	path := filepath.Join(home, ".config/fennec/preparation-input-digests.json")
	var previous map[string]string
	data, err := os.ReadFile(path)
	known := err == nil && len(data) <= 64<<10 && json.Unmarshal(data, &previous) == nil && len(previous) > 0
	changed := []string{}
	if known {
		for name, digest := range digests {
			if previous[name] != digest {
				changed = append(changed, name)
			}
		}
		for name := range previous {
			if _, ok := digests[name]; !ok {
				changed = append(changed, name)
			}
		}
	}
	sort.Strings(changed)
	_ = writePublicJSON(path, digests)
	return changed, known
}

func canonicalPreparationFile(name string, data []byte) any {
	if strings.HasSuffix(name, ".toml") {
		var value map[string]any
		if toml.Unmarshal(data, &value) == nil {
			return value
		}
	} else if json.Valid(data) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&value) == nil {
			return value
		}
	}
	return data
}
