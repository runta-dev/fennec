package cli

import (
	"encoding/base64"
	"fennec/internal/setup"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPreparationDiagnosticsIdentifyChangesWithoutStoringCredentials(t *testing.T) {
	home := t.TempDir()
	request := setup.ModelAccountsRequest{Files: map[string]string{"codex/auth.json": base64.StdEncoding.EncodeToString([]byte(`{"token":"private-fixture","mode":"oauth"}`))}}
	record := func(token string) ([]string, bool) {
		return recordPreparationInputs(home, token, "runtime", false, []setup.Agent{{ID: "codex"}}, nil, nil, request)
	}
	if changed, known := record("account-secret-fixture"); known || len(changed) != 0 {
		t.Fatal(changed, known)
	}
	if changed, known := record("account-secret-fixture"); !known || len(changed) != 0 {
		t.Fatal("stable input changed", changed, known)
	}
	path := filepath.Join(home, ".config/fennec/preparation-input-digests.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-fixture") || strings.Contains(string(data), "account-secret-fixture") || strings.Contains(string(data), request.Files["codex/auth.json"]) {
		t.Fatal("diagnostics exposed credentials")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("diagnostics are not private", err)
	}
	request.Files["codex/auth.json"] = base64.StdEncoding.EncodeToString([]byte("{\n  \"mode\": \"oauth\", \"token\": \"private-fixture\"\n}"))
	changed, known := record("account-secret-fixture")
	if !known || !slices.Equal(changed, []string{"native/codex/auth.json"}) {
		t.Fatal("formatting confused with credential change", changed)
	}
	request.Files["codex/auth.json"] = base64.StdEncoding.EncodeToString([]byte(`{"token":"rotated-fixture","mode":"oauth"}`))
	changed, _ = record("account-secret-fixture")
	if !slices.Equal(changed, []string{"native/codex/auth.json", "native_semantic/codex/auth.json"}) {
		t.Fatal("rotation not identified", changed)
	}
	changed, _ = record("different-account-secret")
	if !slices.Equal(changed, []string{"account"}) {
		t.Fatal("account change not isolated", changed)
	}
}
