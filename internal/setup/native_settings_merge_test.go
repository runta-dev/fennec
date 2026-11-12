package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A settings file edited on the Mac while the cloud copy also changed must not
// block preparation: local preferences win, the cloud model choice and
// cloud-only keys stay.
func TestNativeSettingsChangedOnBothComputersMerges(t *testing.T) {
	remote := t.TempDir()
	encode := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	req := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{"claude/settings.json": encode(`{"theme":"dark","model":"sonnet"}`)}}
	if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(remote, ".local/fennec/native/claude/settings.json")
	// Cloud side: model switched in Fennec and the agent added its own key.
	if err := os.WriteFile(target, []byte(`{"theme":"dark","model":"opus","cloudOnly":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Mac side: the user changed a preference.
	req.Files["claude/settings.json"] = encode(`{"theme":"light","model":"sonnet","statusLine":{"type":"command"}}`)
	if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
		t.Fatalf("settings conflict blocked preparation: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["theme"] != "light" || got["statusLine"] == nil {
		t.Fatalf("local preference edits lost: %s", raw)
	}
	if got["model"] != "opus" {
		t.Fatalf("cloud model choice overwritten: %s", raw)
	}
	if got["cloudOnly"] != true {
		t.Fatalf("cloud-only key dropped: %s", raw)
	}
	// A further sync with the same local file is stable.
	if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
}

func TestMergeNativeSettingsRefusesCredentialFiles(t *testing.T) {
	if _, ok := mergeNativeSettings("codex/auth.json", []byte(`{}`), []byte(`{}`)); ok {
		t.Fatal("credential file merged")
	}
}
