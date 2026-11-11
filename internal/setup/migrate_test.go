package setup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func migrateFixture(home, name, value string) (CredentialMigrationReport, error) {
	payload, _ := json.Marshal(map[string]string{name: base64.StdEncoding.EncodeToString([]byte(value))})
	return ReceiveCredentials(home, bytes.NewReader(payload))
}
func TestCredentialMigrationPreservesRemoteChanges(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	run := func(value string) error { _, err := migrateFixture(home, "providers.json", value); return err }
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
		t.Fatal(err)
	}
	if run(`{"fake":"three"}`) == nil {
		t.Fatal("overwrote remote refresh")
	}
	data, _ := os.ReadFile(filepath.Join(root, "providers.json"))
	if string(data) != `{"remote":"refreshed"}` {
		t.Fatal("remote credentials changed")
	}
}
func TestPluginMigrationPreservesUnrelatedEntries(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	os.MkdirAll(root, 0700)
	target := filepath.Join(root, "plugin-auth.json")
	os.WriteFile(target, []byte(`{"unrelated":{"fake":"keep"}}`), 0600)
	if _, err := migrateFixture(home, "plugin-auth.json", `{"devin":{"fake":"migrate"}}`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	var result map[string]json.RawMessage
	json.Unmarshal(data, &result)
	if len(result) != 2 || result["unrelated"] == nil || result["devin"] == nil {
		t.Fatal("plugin entries lost")
	}
}
func TestLoginMergeRetainsRemoteRefreshAndAddsNewAccount(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	os.MkdirAll(root, 0700)
	target := filepath.Join(root, "logins.json")
	os.WriteFile(target, []byte(`[{"agent":"codex","id":"one","user":"a","auth":{"fake":"refreshed"}},{"agent":"codex","id":"removed","user":"r","hidden":"removed"}]`), 0600)
	source := `[{"agent":"codex","id":"one","user":"a","auth":{"fake":"old"}},{"agent":"codex","id":"two","user":"b","auth":{"fake":"new"}},{"agent":"codex","id":"removed","user":"r","auth":{"fake":"old"}},{"agent":"plugin:devin","id":"plugin","auth":{"fake":"skip"}},{"agent":"codex","id":"local","home":"/local","auth":{"fake":"skip"}}]`
	for i := 0; i < 2; i++ {
		if _, err := migrateFixture(home, "logins.json", source); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(target)
	var entries []map[string]any
	json.Unmarshal(data, &entries)
	if len(entries) != 3 || entries[0]["auth"].(map[string]any)["fake"] != "refreshed" || entries[1]["hidden"] != "removed" {
		t.Fatal("remote identity preservation failed")
	}
}
func TestConcurrentLoginRefreshDoesNotFailEnvironment(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".config/magpie/logins.json")
	run := func(secret string) (CredentialMigrationReport, error) {
		data, _ := json.Marshal([]map[string]any{{"agent": "codex", "id": "one", "user": "test", "auth": map[string]string{"fake": secret}}})
		return migrateFixture(home, "logins.json", string(data))
	}
	for _, value := range []string{"initial", "local-refresh"} {
		if _, err := run(value); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(target)
	remote := strings.ReplaceAll(string(data), "local-refresh", "remote-refresh")
	os.WriteFile(target, []byte(remote), 0600)
	if _, err := run("local-refresh"); err != nil {
		t.Fatal(err)
	}
	report, err := run("conflicting-refresh")
	if err != nil || report.Preserved != 1 {
		t.Fatal("normal refresh blocked setup", report, err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != remote {
		t.Fatal("remote refresh overwritten")
	}
	if _, err := run("a-later-local-refresh"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	if !bytes.Contains(data, []byte("a-later-local-refresh")) {
		t.Fatal("subsequent local update stuck behind obsolete baseline")
	}
}
func fakeLoginAuth(account string, issued int64) map[string]any {
	payload, _ := json.Marshal(map[string]any{"iss": "test", "sub": "user", "iat": issued, "exp": issued + 3600, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	return map[string]any{"tokens": map[string]string{"access_token": "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".fake"}}
}
func TestSameAccountUsesNewerTokensWithoutCrossAccountReplacement(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".config/magpie/logins.json")
	now := time.Now().Unix()
	entry := func(account string, issued int64) string {
		data, _ := json.Marshal([]map[string]any{{"agent": "codex", "id": "one", "user": "test", "auth": fakeLoginAuth(account, issued)}})
		return string(data)
	}
	if _, err := migrateFixture(home, "logins.json", entry("account", now-100)); err != nil {
		t.Fatal(err)
	}
	remote := entry("account", now-10)
	os.WriteFile(target, []byte(remote), 0600)
	if _, err := migrateFixture(home, "logins.json", entry("account", now-22)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	value, _ := parseCredentialJSON(data)
	identity, issued, _, ok := subscriptionTokenMetadata(value.([]any)[0].(map[string]any)["auth"])
	if !ok || issued != now-10 {
		t.Fatal("newer remote token replaced")
	}
	if _, err := migrateFixture(home, "logins.json", entry("account", now)); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	value, _ = parseCredentialJSON(data)
	_, issued, _, _ = subscriptionTokenMetadata(value.([]any)[0].(map[string]any)["auth"])
	if issued != now {
		t.Fatal("newer local token ignored")
	}
	report, err := migrateFixture(home, "logins.json", entry("another-account", now+1))
	if err != nil || report.Ambiguous != 1 {
		t.Fatal(report, err)
	}
	data, _ = os.ReadFile(target)
	value, _ = parseCredentialJSON(data)
	after, _, _, _ := subscriptionTokenMetadata(value.([]any)[0].(map[string]any)["auth"])
	if after != identity {
		t.Fatal("different account overwrote existing login")
	}
}
func TestLegacyCredentialBaselineEncoding(t *testing.T) {
	value, _ := parseCredentialJSON([]byte(`{"z":[1.0,true,null],"a":"中\n"}`))
	if got := string(legacyCredentialJSON(value)); got != `{"a": "\u4e2d\n", "z": [1.0, true, null]}` {
		t.Fatal(got)
	}
}
func TestMissingUserDoesNotMatchUnrelatedLogins(t *testing.T) {
	home := t.TempDir()
	if _, err := migrateFixture(home, "logins.json", `[{"agent":"codex","id":"one","auth":{"fake":"one"}},{"agent":"codex","id":"two","auth":{"fake":"two"}}]`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".config/magpie/logins.json"))
	var values []any
	json.Unmarshal(data, &values)
	if len(values) != 2 {
		t.Fatal("missing users merged unrelated identities")
	}
}
func TestCredentialConcurrentWriterIsPreserved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "logins.json")
	os.WriteFile(path, []byte("new-remote"), 0600)
	err := atomicCredential(path, []byte("incoming"), credentialWrite{path: path, previous: []byte("old-remote"), existed: true})
	if err != errCredentialChanged {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new-remote" {
		t.Fatal("concurrent writer lost")
	}
}

func TestEmptySubscriptionAuthCannotEraseRemote(t *testing.T) {
	home := t.TempDir()
	original := `[{"agent":"codex","id":"one","auth":{"fake":"keep"}}]`
	if _, err := migrateFixture(home, "logins.json", original); err != nil {
		t.Fatal(err)
	}
	for _, empty := range []string{`{}`, `null`, `[]`, `""`} {
		if _, err := migrateFixture(home, "logins.json", `[{"agent":"codex","id":"one","auth":`+empty+`}]`); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(home, ".config/magpie/logins.json"))
	if !bytes.Contains(data, []byte("keep")) {
		t.Fatal("empty authentication replaced valid remote credentials")
	}
}
