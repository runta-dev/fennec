package setup

import (
	"context"
	"encoding/base64"
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
	if accounts := agyAccounts(func(name string) []byte { data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))); return data }); len(accounts) != 1 || accounts[0].Provider != "google" {
		t.Fatal(accounts)
	}
}
