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

func TestVendorNativeAccountsIsolation(t *testing.T) {
	home := t.TempDir()
	for name, data := range map[string]string{
		".cursor/cli-config.json": `{"version":1,"model":{"modelId":"auto"},"hooks":{"run":"local-command"},"permissions":{"allow":["Shell(*)"]}}`,
		".grok/auth.json":         `{"https://accounts.x.ai/sign-in":{"key":"grok-token","auth_mode":"oidc","refresh_token":"grant","email":"grok@example.test"}}`,
		".grok/config.toml":       "[models]\ndefault='native'\n[model.native]\nmodel='grok-native'\nbase_url='https://api.x.ai/v1'\n[hooks]\nrun='local-command'\n",
	} {
		path := filepath.Join(home, name)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte(data), 0600)
	}
	source := NativeSource{Home: home, Env: func(k string) string {
		if k == "CURSOR_API_KEY" {
			return "cursor-key"
		}
		if k == "OPENAI_API_KEY" {
			return "unrelated"
		}
		return ""
	}, Keychain: func(_ context.Context, service, account string) ([]byte, error) {
		if account != "cursor-user" {
			t.Fatal(account)
		}
		switch service {
		case "cursor-access-token":
			return []byte("cursor-token"), nil
		case "cursor-refresh-token":
			return []byte("cursor-grant"), nil
		}
		return nil, os.ErrNotExist
	}}
	request, err := source.GatherAll(context.Background(), []string{"cursor", "grok"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cursorConfigFile, grokConfigFile} {
		data, _ := base64.StdEncoding.DecodeString(request.Files[name])
		if strings.Contains(string(data), "local-command") || strings.Contains(string(data), "permissions") {
			t.Fatalf("local execution settings copied: %s", name)
		}
	}
	cloud := t.TempDir()
	result, err := ApplyNativeAccounts(context.Background(), cloud, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cursor", "grok"} {
		if !result.NativeReady[id] {
			t.Fatalf("not ready: %s", id)
		}
		env, err := nativeAgentEnvironment(cloud, id)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		if strings.Contains(joined, "unrelated") {
			t.Fatal("cross-agent credentials leaked")
		}
		if id == "grok" && strings.Contains(joined, "cursor-key") {
			t.Fatal("Cursor key leaked into Grok")
		}
		view, err := NativeCloudAccount(cloud, id)
		if err != nil || view.Status != "signed_in" {
			t.Fatal(view, err)
		}
	}
	t.Setenv("GROK_AUTH_PATH", "/outside/auth.json")
	t.Setenv("CURSOR_DATA_DIR", "/outside/data")
	t.Setenv("GROK_AUTH_PROVIDER_COMMAND", "untrusted-command")
	env, err := nativeAgentEnvironment(cloud, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "/outside/") || strings.Contains(joined, "untrusted-command") {
		t.Fatal("inherited configuration escaped isolation")
	}
	if !strings.Contains(joined, "XDG_CONFIG_HOME="+filepath.Join(cloud, ".local/fennec/native")) || !strings.Contains(joined, "AGENT_CLI_CREDENTIAL_STORE=file") {
		t.Fatal("Cursor file credentials not selected")
	}
}

func TestVendorLoginsWithMagpie(t *testing.T) {
	for _, id := range []string{"cursor", "grok"} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			if err := ConfigureAgentMode(home, false); err != nil {
				t.Fatal(err)
			}
			plan := NativeLogin{Agent: id, ID: "vendor-login"}
			if err := PrepareNativeLogin(context.Background(), home, plan); err != nil {
				t.Fatal(err)
			}
			args, env, err := NativeLoginCommand(home, plan)
			if err != nil {
				t.Fatal(err)
			}
			if args[0] != "login" {
				t.Fatal(args)
			}
			if id == "grok" && strings.Join(args, " ") != "login --device-auth" {
				t.Fatal(args)
			}
			if !strings.Contains(strings.Join(env, "\n"), filepath.Join(home, ".local/fennec/native-logins/vendor-login", id)) {
				t.Fatal("login not isolated")
			}
			name, data := cursorAuthFile, `{"accessToken":"token","refreshToken":"grant"}`
			if id == "grok" {
				name, data = grokAuthFile, `{"xai::api_key":{"key":"api-key","auth_mode":"api_key"}}`
			}
			if err := os.WriteFile(filepath.Join(nativeLoginDirectory(home, plan.ID), name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := ActivateNativeLogin(context.Background(), home, plan); err != nil {
				t.Fatal(err)
			}
			_, cloud, err := NativeAccountRoot(home, id)
			if err != nil || !cloud {
				t.Fatal(cloud, err)
			}
			if NativeAgentMode(home) {
				t.Fatal("changed global Magpie mode")
			}
		})
	}
}

func TestVendorCredentialConflictPreservesRefresh(t *testing.T) {
	home := t.TempDir()
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	request := ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"cursor"}, Files: map[string]string{"env.json": encode(`{}`), cursorAuthFile: encode(`{"accessToken":"old","refreshToken":"grant"}`)}}
	if _, err := ApplyNativeAccounts(context.Background(), home, request); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".local/fennec/native", cursorAuthFile)
	os.WriteFile(path, []byte(`{"accessToken":"refreshed","refreshToken":"rotated"}`), 0600)
	if _, err := ApplyNativeAccounts(context.Background(), home, request); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var auth map[string]string
	json.Unmarshal(data, &auth)
	if auth["accessToken"] != "refreshed" {
		t.Fatal("overwrote cloud refresh")
	}
	request.Files[cursorAuthFile] = encode(`{"accessToken":"different","refreshToken":"other"}`)
	result, err := ApplyNativeAccounts(context.Background(), home, request)
	if err != nil || len(result.NativeDeferred) != 1 {
		t.Fatal(result.NativeDeferred, err)
	}
}

func TestCursorCredentialStampTracksAllKeychainItems(t *testing.T) {
	modified := map[string]string{}
	store := NativeCredentialStore{KeychainModified: func(_ context.Context, service, account string) (string, error) {
		return modified[service], nil
	}}
	location := NativeCredentialLocation{Name: cursorAuthFile, Service: "cursor-access-token", Account: "cursor-user"}
	previous := store.Modified(context.Background(), location)
	for _, service := range []string{"cursor-access-token", "cursor-refresh-token", "cursor-api-key"} {
		modified[service] = "changed"
		next := store.Modified(context.Background(), location)
		if next == previous {
			t.Fatalf("did not detect change to %s", service)
		}
		previous = next
	}
}
