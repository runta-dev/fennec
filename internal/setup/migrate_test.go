package setup

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialMigrationPreservesRemoteChanges(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	os.MkdirAll(root, 0700)
	// pathlib.Path.home honors HOME; use fake credentials only.
	run := func(value string) error {
		payload, _ := json.Marshal(map[string]string{"providers.json": base64.StdEncoding.EncodeToString([]byte(value))})
		cmd := exec.Command("python3", "-c", migrateReceiver)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(string(payload))
		return cmd.Run()
	}
	if err := run(`{"fake":"one"}`); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(root, "providers.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions")
	}
	if err := run(`{"fake":"two"}`); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "providers.json"), []byte(`{"remote":"refreshed"}`), 0600)
	if err := run(`{"fake":"two"}`); err != nil {
		t.Fatal("unchanged local source must preserve refreshed remote", err)
	}
	if run(`{"fake":"three"}`) == nil {
		t.Fatal("overwrote remote refresh")
	}
	data, _ := os.ReadFile(filepath.Join(root, "providers.json"))
	if string(data) != `{"remote":"refreshed"}` {
		t.Fatal("lost remote credentials")
	}
}

func TestPluginMigrationPreservesUnrelatedEntries(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	os.MkdirAll(root, 0700)
	target := filepath.Join(root, "plugin-auth.json")
	os.WriteFile(target, []byte(`{"unrelated":{"fake":"keep"}}`), 0600)
	payload, _ := json.Marshal(map[string]string{"plugin-auth.json": base64.StdEncoding.EncodeToString([]byte(`{"devin":{"fake":"migrate"}}`))})
	cmd := exec.Command("python3", "-c", migrateReceiver)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(string(payload))
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	var result map[string]json.RawMessage
	json.Unmarshal(data, &result)
	if len(result) != 2 || result["unrelated"] == nil || result["devin"] == nil {
		t.Fatal("lost plugin entries")
	}
}

func TestLoginMergeRetainsRemoteRefreshAndAddsNewAccount(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	os.MkdirAll(root, 0700)
	target := filepath.Join(root, "logins.json")
	os.WriteFile(target, []byte(`[{"agent":"codex","id":"one","user":"a","auth":{"fake":"refreshed"}},{"agent":"codex","id":"removed","user":"r","hidden":"removed"}]`), 0600)
	source := `[{"agent":"codex","id":"one","user":"a","auth":{"fake":"old"}},{"agent":"codex","id":"two","user":"b","auth":{"fake":"new"}},{"agent":"codex","id":"removed","user":"r","auth":{"fake":"old"}},{"agent":"plugin:devin","id":"plugin","auth":{"fake":"skip"}},{"agent":"codex","id":"local","home":"/local","auth":{"fake":"skip"}}]`
	payload, _ := json.Marshal(map[string]string{"logins.json": base64.StdEncoding.EncodeToString([]byte(source))})
	for i := 0; i < 2; i++ {
		cmd := exec.Command("python3", "-c", migrateReceiver)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(string(payload))
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(target)
	var entries []map[string]any
	json.Unmarshal(data, &entries)
	if len(entries) != 3 || entries[0]["auth"].(map[string]any)["fake"] != "refreshed" || entries[1]["hidden"] != "removed" {
		t.Fatalf("merge violated preservation: %s", data)
	}
}

func TestLoginMigrationUpdatesOnlyUnchangedRemoteCredentials(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".config/magpie/logins.json")
	run := func(secret string) error {
		entries, _ := json.Marshal([]map[string]any{{"agent": "codex", "id": "one", "user": "test", "auth": map[string]string{"fake": secret}}})
		payload, _ := json.Marshal(map[string]string{"logins.json": base64.StdEncoding.EncodeToString(entries)})
		command := exec.Command("python3", "-c", migrateReceiver)
		command.Env = append(os.Environ(), "HOME="+home)
		command.Stdin = strings.NewReader(string(payload))
		return command.Run()
	}
	for _, value := range []string{"initial", "local-refresh"} {
		if err := run(value); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "local-refresh") {
		t.Fatal("local refresh was ignored")
	}
	remote := strings.ReplaceAll(string(data), "local-refresh", "remote-refresh")
	if err := os.WriteFile(target, []byte(remote), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("local-refresh"); err != nil {
		t.Fatal("unchanged local source rejected", err)
	}
	if err := run("conflicting-refresh"); err == nil {
		t.Fatal("conflicting refresh silently overwrote remote")
	}
	data, _ = os.ReadFile(target)
	if string(data) != remote {
		t.Fatal("remote credentials were changed")
	}
}
