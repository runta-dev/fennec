package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type nativeLoginPreferences struct {
	Runtime string            `json:"runtime"`
	Agents  map[string]string `json:"agents"`
}

// A device-side discovery hint, never proof of authentication. Readiness is
// determined from credentials in the selected account of the owned runtime.
func NativeCloudLogins(directory, user, runtime string) map[string]string {
	path, err := preferencePath(directory, user)
	if err != nil {
		return map[string]string{}
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "native-accounts.json"))
	if err != nil {
		return map[string]string{}
	}
	var preferences nativeLoginPreferences
	if json.Unmarshal(data, &preferences) != nil || preferences.Runtime != runtime || preferences.Agents == nil {
		return map[string]string{}
	}
	for agent, id := range preferences.Agents {
		if (NativeLogin{Agent: agent, ID: id}).Validate() != nil {
			return map[string]string{}
		}
	}
	return preferences.Agents
}
func NativeCloudPreferences(directory, user, runtime string) map[string]bool {
	result := map[string]bool{}
	for agent := range NativeCloudLogins(directory, user, runtime) {
		result[agent] = true
	}
	return result
}
func SaveNativeCloudPreference(directory, user, runtime, agent, id string) error {
	if err := (NativeLogin{Agent: agent, ID: id}).Validate(); err != nil {
		return err
	}
	path, err := preferencePath(directory, user)
	if err != nil {
		return err
	}
	agents := NativeCloudLogins(directory, user, runtime)
	agents[agent] = id
	data, _ := json.Marshal(nativeLoginPreferences{Runtime: runtime, Agents: agents})
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicCredential(filepath.Join(filepath.Dir(path), "native-accounts.json"), data)
}

// Only public login session identities, never credential contents.
func SelectedNativeLogins(home string) (map[string]string, error) { return readCloudLogins(home) }
