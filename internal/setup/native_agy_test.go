package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const agyTestToken = `{"token":{"access_token":"ya29.mac","token_type":"Bearer","refresh_token":"1//mac","expiry":"2026-11-25T10:00:00Z"},"auth_method":"consumer","extra":"dropped"}`

func TestAgyTokenReadsKeychainAndFileForms(t *testing.T) {
	keyring := agyKeyringPrefix + base64.StdEncoding.EncodeToString([]byte(agyTestToken))
	for _, raw := range []string{agyTestToken, keyring, keyring + "\n"} {
		token, ok := agyToken([]byte(raw))
		if !ok || strings.Contains(string(token), "extra") || !strings.Contains(string(token), `"auth_method":"consumer"`) || !strings.Contains(string(token), `"refresh_token":"1//mac"`) {
			t.Fatalf("%q -> %s %v", raw, token, ok)
		}
	}
	for _, raw := range []string{"", "{}", `{"token":{}}`, agyKeyringPrefix + "!!", `{"token":{"token_type":"Bearer"}}`} {
		if _, ok := agyToken([]byte(raw)); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestAgyGatherPrefersKeychainThenFile(t *testing.T) {
	home := t.TempDir()
	keychain := func(_ context.Context, service, account string) ([]byte, error) {
		if service != agyKeychainService || account != agyKeychainAccount {
			return nil, os.ErrNotExist
		}
		return []byte(agyKeyringPrefix + base64.StdEncoding.EncodeToString([]byte(agyTestToken))), nil
	}
	source := NativeSource{Home: home, Keychain: keychain}
	request, err := source.Gather(context.Background(), []string{"agy"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(request.Files[agyAuthFile])
	if !strings.Contains(string(raw), "ya29.mac") {
		t.Fatalf("keychain sign-in not gathered: %s", raw)
	}
	none := NativeSource{Home: home}
	if request, err := none.Gather(context.Background(), []string{"agy"}); err != nil || request.Files[agyAuthFile] != "" {
		t.Fatal("gathered a sign-in the Mac does not have", err)
	}
	if err := os.MkdirAll(filepath.Dir(agyTokenPath(home)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agyTokenPath(home), []byte(strings.ReplaceAll(agyTestToken, "ya29.mac", "ya29.file")), 0600); err != nil {
		t.Fatal(err)
	}
	request, err = none.Gather(context.Background(), []string{"agy"})
	raw, _ = base64.StdEncoding.DecodeString(request.Files[agyAuthFile])
	if err != nil || !strings.Contains(string(raw), "ya29.file") {
		t.Fatalf("file sign-in not gathered: %s %v", raw, err)
	}
	locations := NativeCredentialStore{Source: source}.Locations([]string{"agy"})
	if len(locations) != 1 || !locations[0].ReadOnly || locations[0].Service != agyKeychainService || locations[0].Name != agyAuthFile {
		t.Fatalf("agy credential location %+v", locations)
	}
	if NativeCredentialAgent(agyAuthFile) != "agy" {
		t.Fatal("agy sign-in is not synchronized")
	}
}

func TestInstallAgyTokenKeepsCloudRefresh(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local/fennec/native")
	if err := os.MkdirAll(filepath.Join(root, "agy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := installAgyToken(home, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agyTokenPath(home)); !os.IsNotExist(err) {
		t.Fatal("wrote a sign-in without a synced copy")
	}
	synced := filepath.Join(root, filepath.FromSlash(agyAuthFile))
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(agyTokenPath(home))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	write(synced, agyTestToken)
	if err := installAgyToken(home, root); err != nil || !strings.Contains(read(), "ya29.mac") {
		t.Fatal("Mac sign-in not placed", err)
	}
	if info, _ := os.Stat(agyTokenPath(home)); info.Mode().Perm() != 0600 {
		t.Fatal("sign-in readable by others", info.Mode())
	}
	refreshed := strings.NewReplacer("ya29.mac", "ya29.cloud", "2026-11-25T10:00:00Z", "2026-11-25T12:00:00Z").Replace(agyTestToken)
	write(agyTokenPath(home), refreshed)
	if err := installAgyToken(home, root); err != nil || !strings.Contains(read(), "ya29.cloud") {
		t.Fatal("cloud refresh overwritten by an older Mac copy", err)
	}
	other := strings.NewReplacer("ya29.mac", "ya29.other", "1//mac", "1//other").Replace(agyTestToken)
	write(synced, other)
	if err := installAgyToken(home, root); err != nil || !strings.Contains(read(), "ya29.other") {
		t.Fatal("new Mac sign-in did not replace the cloud's", err)
	}
	if accounts := agyAccounts(func(name string) []byte {
		data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		return data
	}); len(accounts) != 1 || accounts[0].Provider != "google" {
		t.Fatal(accounts)
	}
}

// agy 1.5.17 on macOS, signed in with a Google Cloud project (Agent Platform).
const agyGCPToken = `{"token":{"access_token":"ya29.gcp","token_type":"Bearer","refresh_token":"1//gcp","expiry":"2026-11-25T10:00:00Z","expires_in":3599},"auth_method":"gcp","id_token_email":"someone@example.com","project_id":"playground-1","region":"global","unrelated":"dropped"}`

func TestAgyKeychainValueIsDecoded(t *testing.T) {
	value := []byte(agyKeyringPrefix + base64.StdEncoding.EncodeToString([]byte(agyGCPToken)) + "\n")
	data, err := decodeNativeKeychain(agyKeychainService, value)
	if err != nil || !strings.Contains(string(data), `"project_id":"playground-1"`) {
		t.Fatalf("go-keyring value not decoded: %q %v", data, err)
	}
	if _, err := decodeNativeKeychain(agyKeychainService, []byte(agyKeyringPrefix+"!!")); err == nil {
		t.Fatal("accepted an undecodable value")
	}
	home := t.TempDir()
	source := NativeSource{Home: home, Keychain: func(_ context.Context, service, _ string) ([]byte, error) {
		return decodeNativeKeychain(service, value)
	}}
	request, err := source.GatherAll(context.Background(), []string{"agy"})
	if err != nil || request.NativeProblems["agy"] != "" || request.Files[agyAuthFile] == "" {
		t.Fatalf("agy sign-in unavailable: %v %v", err, request.NativeProblems)
	}
	view := NativeAccountFromFiles("agy", request.Files)
	if view.Status != "signed_in" || len(view.Accounts) != 1 || view.Accounts[0].Email != "someone@example.com" || view.Accounts[0].Subscription != "Agent Platform" || view.Accounts[0].Project != "playground-1" {
		t.Fatalf("%+v", view)
	}
}

func TestAgyGCPSignInKeepsProject(t *testing.T) {
	token, ok := agyToken([]byte(agyGCPToken))
	if !ok {
		t.Fatal("GCP sign-in rejected")
	}
	for _, want := range []string{`"auth_method":"gcp"`, `"project_id":"playground-1"`, `"region":"global"`, `"id_token_email":"someone@example.com"`, `"expires_in":3599`} {
		if !strings.Contains(string(token), want) {
			t.Fatalf("missing %s in %s", want, token)
		}
	}
	if strings.Contains(string(token), "unrelated") {
		t.Fatal("copied a field that is not the sign-in's")
	}
}

// Preparation marks agy ready to run on the cloud once its sign-in is there;
// without it the Mac's launcher refuses with "cloud credentials need attention".
func TestAgyNativeReadyWithMigratedSignIn(t *testing.T) {
	root := t.TempDir()
	if nativeHasCredentials(root, "agy") {
		t.Fatal("ready without a sign-in")
	}
	if err := os.MkdirAll(filepath.Join(root, "agy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(agyAuthFile)), []byte(agyGCPToken), 0600); err != nil {
		t.Fatal(err)
	}
	if !nativeHasCredentials(root, "agy") {
		t.Fatal("migrated sign-in not counted")
	}
}

func TestTrustAgyWorkspaceKeepsSettings(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini/antigravity-cli/settings.json")
	if err := TrustAgyWorkspace(home, "/root/.fen/a"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"model":"Gemini 4 Neon (Medium)","trustedWorkspaces":["/root/.fen/a"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/root/.fen/b/", "/root/.fen/a", "relative"} {
		if err := TrustAgyWorkspace(home, dir); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	var settings struct {
		Model   string   `json:"model"`
		Trusted []string `json:"trustedWorkspaces"`
	}
	if json.Unmarshal(data, &settings) != nil || settings.Model != "Gemini 4 Neon (Medium)" || strings.Join(settings.Trusted, ",") != "/root/.fen/a,/root/.fen/b" {
		t.Fatalf("%s", data)
	}
	os.WriteFile(path, []byte(`{broken`), 0600)
	if err := TrustAgyWorkspace(home, "/root/.fen/c"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{broken` {
		t.Fatal("rewrote a file it could not read")
	}
}
