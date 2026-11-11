package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Changes only derived configuration cache metadata, never credential inputs.
// The normal preparation path must still synchronize and verify configuration.
func invalidateConfigurationCheckpoint(root string) error {
	var installed struct {
		Fingerprint string `json:"fingerprint"`
	}
	data, err := os.ReadFile(filepath.Join(root, "installed-runtime.json"))
	if err != nil || json.Unmarshal(data, &installed) != nil || len(installed.Fingerprint) != 64 {
		return fmt.Errorf("verified installation checkpoint required")
	}
	if _, err := hex.DecodeString(installed.Fingerprint); err != nil {
		return err
	}
	path := filepath.Join(root, "prepared-runtime.json")
	data, err = os.ReadFile(path)
	if err != nil {
		return err
	}
	var checkpoint map[string]json.RawMessage
	if len(data) > 2<<20 || json.Unmarshal(data, &checkpoint) != nil || checkpoint == nil {
		return fmt.Errorf("invalid preparation checkpoint")
	}
	diagnostics := filepath.Join(root, "diagnostics")
	if err := os.MkdirAll(diagnostics, 0700); err != nil {
		return err
	}
	backup, err := os.CreateTemp(diagnostics, "profile-configuration-backup-*.json")
	if err != nil {
		return err
	}
	_, err = backup.Write(data)
	closeErr := backup.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	checkpoint["fingerprint"] = json.RawMessage(`"profile-forced-configuration-miss"`)
	file, err := os.CreateTemp(root, ".profile-configuration-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(checkpoint)
	closeErr = file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
