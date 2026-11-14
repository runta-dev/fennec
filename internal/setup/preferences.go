package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func preferencePath(directory, user string) (string, error) {
	if user == "" {
		return "", fmt.Errorf("authenticated user required")
	}
	sum := sha256.Sum256([]byte(user))
	return filepath.Join(directory, "users", hex.EncodeToString(sum[:]), "models.json"), nil
}
func SaveModelPreference(directory, user, agent, model string) error {
	if !map[string]bool{"claude": true, "codex": true, "pi": true, "opencode": true, "kimi": true}[agent] {
		return fmt.Errorf("unsupported agent")
	}
	provider, name, ok := strings.Cut(model, "/")
	if !ok || provider == "" || name == "" || len(model) > 512 || strings.ContainsAny(model, "\x00\r\n") {
		return fmt.Errorf("explicit provider/model required")
	}
	path, err := preferencePath(directory, user)
	if err != nil {
		return err
	}
	values := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &values) != nil {
			return fmt.Errorf("invalid model preference file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	values[agent] = model
	data, _ := json.Marshal(values)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "models-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func ApplyModelPreferences(directory, user string, routes []AgentRoute) ([]AgentRoute, error) {
	path, err := preferencePath(directory, user)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return routes, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if json.Unmarshal(data, &values) != nil {
		return nil, fmt.Errorf("invalid model preference file")
	}
	out := append([]AgentRoute(nil), routes...)
	for i, r := range out {
		if model, ok := values[r.Agent]; ok {
			if _, err := ParseAgentModel("Agent model " + model); err != nil {
				return nil, err
			}
			out[i].Model = model
			out[i].Problem = ""
		}
	}
	return out, nil
}
