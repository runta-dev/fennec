package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type NativeModelChoice struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider string   `json:"provider,omitempty"`
	Efforts  []string `json:"efforts,omitempty"`
}
type NativeModelSelection struct {
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
}
type NativeModelState struct {
	Agent  string              `json:"agent"`
	Model  string              `json:"model"`
	Effort string              `json:"effort,omitempty"`
	Models []NativeModelChoice `json:"models"`
}

func NativeModelFamily(agent, model string) bool {
	if model == "" {
		return true
	}
	if len(model) > 256 || strings.ContainsAny(model, "\x00\r\n\t ") {
		return false
	}
	switch agent {
	case "codex":
		return regexp.MustCompile(`^(gpt-|chatgpt-|codex-|o[1-9](?:-|$))[a-zA-Z0-9._-]*$`).MatchString(model)
	case "claude":
		return model == "sonnet" || model == "opus" || model == "haiku" || model == "opusplan" || regexp.MustCompile(`^claude-[a-zA-Z0-9._-]+(?:\[1m\])?$`).MatchString(model)
	case "pi":
		provider, id, ok := strings.Cut(model, "/")
		return ok && provider != "" && provider != "magpie" && id != ""
	}
	return false
}
func nativeModelID(agent, model string) string {
	for _, prefix := range map[string][]string{"codex": {"codex/", "openai/"}, "claude": {"claude/", "anthropic/"}}[agent] {
		model = strings.TrimPrefix(model, prefix)
	}
	if !NativeModelFamily(agent, model) {
		return ""
	}
	return model
}
func jsonObject(data []byte) map[string]any {
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	if value == nil {
		value = map[string]any{}
	}
	return value
}
func stringValue(value any) string { s, _ := value.(string); return s }

// Native mode owns only the vendor-native model namespace. Invalid inherited
// gateway models are removed, letting the native agent choose its default.
func sanitizeNativeModelFile(name string, data []byte) ([]byte, error) {
	switch name {
	case "codex/config.toml":
		var cfg map[string]any
		if toml.Unmarshal(data, &cfg) != nil {
			return nil, fmt.Errorf("invalid Codex configuration")
		}
		model := nativeModelID("codex", stringValue(cfg["model"]))
		if model == "" {
			delete(cfg, "model")
			delete(cfg, "model_reasoning_effort")
			delete(cfg, "model_reasoning_summary")
			delete(cfg, "model_verbosity")
		} else {
			cfg["model"] = model
		}
		return toml.Marshal(cfg)
	case "claude/settings.json":
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) != nil || cfg == nil {
			return nil, fmt.Errorf("invalid native model settings")
		}
		model := nativeModelID("claude", stringValue(cfg["model"]))
		if model == "" {
			delete(cfg, "model")
		} else {
			cfg["model"] = model
		}
		env, _ := cfg["env"].(map[string]any)
		for _, key := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
			if value, ok := env[key]; ok {
				model := nativeModelID("claude", stringValue(value))
				if model == "" {
					delete(env, key)
				} else {
					env[key] = model
				}
			}
		}
		return json.Marshal(cfg)
	}
	return data, nil
}

func nativeModelFile(agent string) string {
	return map[string]string{"codex": "codex/config.toml", "claude": "claude/settings.json", "pi": "pi/settings.json"}[agent]
}
func nativeSettings(root, agent string) (map[string]any, []byte, error) {
	name := nativeModelFile(agent)
	if name == "" {
		return nil, nil, fmt.Errorf("native model settings unavailable for this agent")
	}
	raw, err := readNativeFile(filepath.Join(root, name))
	if os.IsNotExist(err) {
		return map[string]any{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if agent == "codex" {
		var value map[string]any
		if toml.Unmarshal(raw, &value) != nil {
			return nil, nil, fmt.Errorf("invalid native model settings")
		}
		if value == nil {
			value = map[string]any{}
		}
		return value, raw, nil
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, nil, fmt.Errorf("invalid native model settings")
	}
	return value, raw, nil
}
func NativeModels(ctx context.Context, home, agent string, refresh bool) (NativeModelState, error) {
	state := NativeModelState{Agent: agent, Models: []NativeModelChoice{{ID: "", Name: "Default"}}}
	root, _, err := NativeAccountRoot(home, agent)
	if err != nil {
		return state, err
	}
	cfg, _, err := nativeSettings(root, agent)
	if err != nil {
		return state, err
	}
	state.Model = stringValue(cfg["model"])
	if agent == "codex" {
		state.Model = nativeModelID(agent, state.Model)
		state.Effort = stringValue(cfg["model_reasoning_effort"])
	}
	if agent == "claude" {
		state.Model = nativeModelID(agent, state.Model)
	}
	if agent == "pi" {
		provider, model := stringValue(cfg["defaultProvider"]), stringValue(cfg["defaultModel"])
		if provider != "" && model != "" {
			state.Model = provider + "/" + model
		}
	}
	add := func(choice NativeModelChoice) {
		if !NativeModelFamily(agent, choice.ID) {
			return
		}
		for _, prior := range state.Models {
			if prior.ID == choice.ID {
				return
			}
		}
		if len(state.Models) >= 512 {
			return
		}
		if choice.Name == "" || len(choice.Name) > 256 {
			choice.Name = choice.ID
		}
		state.Models = append(state.Models, choice)
	}
	switch agent {
	case "codex":
		raw, _ := readNativeFile(filepath.Join(root, "codex/models_cache.json"))
		var cache struct {
			Models []struct {
				Slug       string `json:"slug"`
				Name       string `json:"display_name"`
				Visibility string `json:"visibility"`
				Levels     []struct {
					Effort string `json:"effort"`
				} `json:"supported_reasoning_levels"`
			} `json:"models"`
		}
		if json.Unmarshal(raw, &cache) == nil {
			for _, m := range cache.Models {
				if m.Visibility == "hide" {
					continue
				}
				choice := NativeModelChoice{ID: m.Slug, Name: m.Name, Provider: "openai"}
				for _, level := range m.Levels {
					if regexp.MustCompile(`^[a-z]{1,16}$`).MatchString(level.Effort) {
						choice.Efforts = append(choice.Efforts, level.Effort)
					}
				}
				add(choice)
			}
		}
	case "claude":
		for _, id := range []string{"sonnet", "opus", "haiku"} {
			add(NativeModelChoice{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Provider: "anthropic"})
		}
		raw, _ := readNativeFile(filepath.Join(root, "claude/.claude.json"))
		var cache struct {
			Options []struct {
				Value    string `json:"value"`
				Label    string `json:"label"`
				Disabled bool   `json:"disabled"`
			} `json:"additionalModelOptionsCache"`
			Access []struct {
				Name     string `json:"apiName"`
				Entitled bool   `json:"entitled"`
			} `json:"modelAccessCache"`
		}
		if json.Unmarshal(raw, &cache) == nil {
			denied := map[string]bool{}
			for _, a := range cache.Access {
				if !a.Entitled {
					denied[a.Name] = true
				} else {
					add(NativeModelChoice{ID: a.Name, Name: a.Name, Provider: "anthropic"})
				}
			}
			for _, m := range cache.Options {
				if !m.Disabled && !denied[m.Value] {
					add(NativeModelChoice{ID: m.Value, Name: m.Label, Provider: "anthropic"})
				}
			}
		}
	case "pi":
		providers := nativePiProviders(root)
		provider, _, _ := strings.Cut(state.Model, "/")
		if !providers[provider] || !NativeModelFamily(agent, state.Model) {
			state.Model = ""
			state.Effort = ""
		}
		raw, _ := readNativeFile(filepath.Join(root, "pi/models.json"))
		var custom struct {
			Providers map[string]struct {
				Models []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"models"`
			} `json:"providers"`
		}
		_ = json.Unmarshal(raw, &custom)
		for provider, cfg := range custom.Providers {
			if providers[provider] {
				for _, model := range cfg.Models {
					add(NativeModelChoice{ID: provider + "/" + model.ID, Name: model.Name, Provider: provider})
				}
			}
		}
		if refresh {
			for _, choice := range piListedModels(ctx, home) {
				if providers[choice.Provider] {
					add(choice)
				}
			}
		}
	}
	return state, nil
}
func nativePiProviders(root string) map[string]bool {
	return nativePiProviderFiles(func(name string) []byte { data, _ := readNativeFile(filepath.Join(root, name)); return data })
}
func nativePiProviderFiles(read func(string) []byte) map[string]bool {

	connected := map[string]bool{}
	raw := read("pi/auth.json")
	for provider, value := range jsonObject(raw) {
		entry, _ := value.(map[string]any)
		if stringValue(entry["key"]) != "" || stringValue(entry["access"]) != "" {
			connected[provider] = true
		}
	}
	raw = read("env.json")
	env := jsonObject(raw)
	for key, provider := range map[string]string{"OPENAI_API_KEY": "openai", "ANTHROPIC_API_KEY": "anthropic", "GEMINI_API_KEY": "google", "GOOGLE_API_KEY": "google", "OPENROUTER_API_KEY": "openrouter", "GROQ_API_KEY": "groq", "XAI_API_KEY": "xai", "MISTRAL_API_KEY": "mistral"} {
		if stringValue(env[key]) != "" {
			connected[provider] = true
		}
	}
	raw = read("pi/models.json")
	cfg := jsonObject(raw)
	providers, _ := cfg["providers"].(map[string]any)
	for provider, value := range providers {
		entry, _ := value.(map[string]any)
		if nativeProviderKey(entry, mapStringValues(env)) {
			connected[provider] = true
		}
	}
	delete(connected, "magpie")
	return connected
}
func mapStringValues(m map[string]any) map[string]string {
	r := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			r[k] = s
		}
	}
	return r
}

type modelOutput struct{ bytes.Buffer }

func (b *modelOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("model list too large")
	}
	return b.Buffer.Write(p)
}
func piListedModels(ctx context.Context, home string) []NativeModelChoice {
	step, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(step, "pi", "--no-extensions", "--no-skills", "--list-models")
	root, _, err := NativeAccountRoot(home, "pi")
	if err != nil {
		return nil
	}
	command.Dir = filepath.Join(root, "pi")
	env, err := nativeAgentEnvironment(home, "pi")
	if err != nil {
		return nil
	}
	command.Env = append(env, "NO_COLOR=1", "TERM=dumb")
	output := &modelOutput{}
	command.Stdout = output
	command.WaitDelay = time.Second
	if command.Run() != nil {
		return nil
	}
	var choices []NativeModelChoice
	for _, line := range strings.Split(output.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "provider" || !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(fields[0]) {
			continue
		}
		id := fields[0] + "/" + fields[1]
		if NativeModelFamily("pi", id) {
			choices = append(choices, NativeModelChoice{ID: id, Name: fields[1], Provider: fields[0]})
		}
	}
	return choices
}
func ValidateNativeSelection(choice NativeModelSelection, state NativeModelState) error {
	if choice.Agent != state.Agent || !NativeModelFamily(choice.Agent, choice.Model) {
		return fmt.Errorf("choose a native model supported by this agent")
	}
	for _, model := range state.Models {
		if model.ID == choice.Model {
			if choice.Effort == "" {
				return nil
			}
			for _, effort := range model.Efforts {
				if effort == choice.Effort {
					return nil
				}
			}
			return fmt.Errorf("reasoning effort is not supported by this model")
		}
	}
	return fmt.Errorf("model is not in this agent's native model list")
}
func writeNativeSelection(home string, choice NativeModelSelection) error {
	root, _, err := NativeAccountRoot(home, choice.Agent)
	if err != nil {
		return err
	}
	cfg, previous, err := nativeSettings(root, choice.Agent)
	if err != nil {
		return err
	}
	switch choice.Agent {
	case "codex":
		if choice.Model == "" {
			delete(cfg, "model")
		} else {
			cfg["model"] = choice.Model
		}
		if choice.Effort == "" {
			delete(cfg, "model_reasoning_effort")
		} else {
			cfg["model_reasoning_effort"] = choice.Effort
		}
	case "claude":
		if choice.Model == "" {
			delete(cfg, "model")
		} else {
			cfg["model"] = choice.Model
		}
	case "pi":
		if choice.Model == "" {
			delete(cfg, "defaultModel")
			delete(cfg, "defaultProvider")
		} else {
			provider, id, _ := strings.Cut(choice.Model, "/")
			cfg["defaultProvider"], cfg["defaultModel"] = provider, id
		}
	}
	var data []byte
	if choice.Agent == "codex" {
		data, err = toml.Marshal(cfg)
	} else {
		data, err = json.Marshal(cfg)
	}
	if err != nil {
		return err
	}
	path := filepath.Join(root, nativeModelFile(choice.Agent))
	relative, err := filepath.Rel(home, filepath.Dir(path))
	if err != nil {
		return err
	}
	if err = ensureNativeDirectory(home, relative); err != nil {
		return err
	}
	marker := filepath.Join(root, ".baseline.json")
	baseline := map[string]credentialPair{}
	raw, readErr := readNativeFile(marker)
	if readErr == nil {
		if json.Unmarshal(raw, &baseline) != nil || baseline == nil {
			return fmt.Errorf("invalid native baseline")
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	if err = atomicCredential(path, data, credentialWrite{previous: previous, existed: previous != nil}); err != nil {
		return err
	}
	value := baseline[nativeModelFile(choice.Agent)]
	value.Remote = credentialHash(data)
	baseline[nativeModelFile(choice.Agent)] = value
	raw, _ = json.Marshal(baseline)
	return atomicCredential(marker, raw)
}
func SetNativeModel(ctx context.Context, home string, choice NativeModelSelection) (NativeModelState, error) {
	if !NativeAgentMode(home) {
		return NativeModelState{}, fmt.Errorf("native model settings require native mode")
	}
	var state NativeModelState
	var err error
	root := filepath.Join(home, ".local/fennec/native")
	if err = ensureNativeDirectory(home, ".local/fennec/native"); err != nil {
		return state, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return state, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return state, err
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if !NativeAgentMode(home) {
		return state, fmt.Errorf("native mode changed")
	}
	state, err = NativeModels(ctx, home, choice.Agent, false)
	if err != nil {
		return state, err
	}
	if err = ValidateNativeSelection(choice, state); err != nil {
		state, err = NativeModels(ctx, home, choice.Agent, true)
		if err != nil {
			return state, err
		}
		if err = ValidateNativeSelection(choice, state); err != nil {
			return state, err
		}
	}
	if err = writeNativeSelection(home, choice); err != nil {
		return state, err
	}
	state.Model = choice.Model
	state.Effort = choice.Effort
	return state, nil
}

// Only model fields may be reconciled across a policy upgrade. Credentials and
// unrelated configuration retain the normal conflict checks.
func compatibleNativeNonModel(name string, a, b []byte) bool {
	decode := func(data []byte) (map[string]any, bool) {
		var cfg map[string]any
		var err error
		if name == "codex/config.toml" {
			err = toml.Unmarshal(data, &cfg)
		} else {
			err = json.Unmarshal(data, &cfg)
		}
		if err != nil || cfg == nil {
			return nil, false
		}
		switch name {
		case "codex/config.toml":
			for _, k := range []string{"model", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity"} {
				delete(cfg, k)
			}
		case "claude/settings.json":
			delete(cfg, "model")
		default:
			return nil, false
		}
		return cfg, true
	}
	x, ok := decode(a)
	if !ok {
		return false
	}
	y, ok := decode(b)
	if !ok {
		return false
	}
	// Cloud agents add their own TUI state. Preserve those extra keys; only
	// overlapping exported non-model fields must agree.
	for key := range x {
		if _, exists := y[key]; !exists && !(name == "codex/config.toml" && (key == "tui" || key == "notice" || key == "projects")) {
			return false
		}
	}
	for key, value := range y {
		ax, _ := json.Marshal(x[key])
		by, _ := json.Marshal(value)
		if !bytes.Equal(ax, by) {
			return false
		}
	}
	return true
}

func sanitizeNativePiSettings(data []byte, providers map[string]bool) ([]byte, error) {
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil || cfg == nil {
		return nil, fmt.Errorf("invalid Pi settings")
	}
	if provider := stringValue(cfg["defaultProvider"]); provider != "" && !providers[provider] {
		delete(cfg, "defaultProvider")
		delete(cfg, "defaultModel")
		delete(cfg, "defaultThinkingLevel")
	}
	return json.Marshal(cfg)
}
