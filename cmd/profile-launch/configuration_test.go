package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationInvalidationPreservesInstallationAndEvents(t *testing.T) {
	root := t.TempDir()
	installed := []byte(`{"fingerprint":"` + strings.Repeat("a", 64) + `"}`)
	original := []byte(`{"fingerprint":"original","events":[{"type":"route_results"}]}`)
	if err := os.WriteFile(filepath.Join(root, "installed-runtime.json"), installed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prepared-runtime.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := invalidateConfigurationCheckpoint(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "installed-runtime.json"))
	if err != nil || !bytes.Equal(data, installed) {
		t.Fatal("installation checkpoint changed")
	}
	data, err = os.ReadFile(filepath.Join(root, "prepared-runtime.json"))
	var checkpoint struct {
		Fingerprint string              `json:"fingerprint"`
		Events      []map[string]string `json:"events"`
	}
	if err != nil || json.Unmarshal(data, &checkpoint) != nil || checkpoint.Fingerprint == "original" || len(checkpoint.Events) != 1 || checkpoint.Events[0]["type"] != "route_results" {
		t.Fatal("configuration invalidation lost cached events")
	}
	backups, err := filepath.Glob(filepath.Join(root, "diagnostics/profile-configuration-backup-*.json"))
	if err != nil || len(backups) != 1 {
		t.Fatal("missing backup")
	}
	data, err = os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("original checkpoint not preserved")
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup not private")
	}
}
