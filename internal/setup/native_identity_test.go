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

func TestNativeOAuthExportIgnoresGatewayOverrides(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			home, remote := t.TempDir(), t.TempDir()
			env := map[string]string{"OPENAI_API_KEY": "proxy-only-key", "OPENAI_BASE_URL": "http://127.0.0.1:3425/v1", "ANTHROPIC_API_KEY": "proxy-only-key", "ANTHROPIC_AUTH_TOKEN": "proxy-only-key", "ANTHROPIC_BASE_URL": "http://localhost:3425"}
			var path, original string
			if agent == "codex" {
				nativeFixture(t, home, ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"native-oauth"}}`)
				path = ".codex/config.toml"
				original = "model_provider='magpie'\nmodel='kimi-code/k3'\n[model_providers.magpie]\nbase_url='http://localhost:3425/v1'\n"
			} else {
				nativeFixture(t, home, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"native-oauth"}}`)
				path = ".claude/settings.json"
				original = `{"model":"codex/gpt-test","env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425","ANTHROPIC_AUTH_TOKEN":"proxy-only-key"}}`
			}
			nativeFixture(t, home, path, original)
			source := NativeSource{Home: home, Env: func(k string) string { return env[k] }}
			request, err := source.GatherAll(context.Background(), []string{agent})
			if err != nil {
				t.Fatal(err)
			}
			if request.NativeProblems[agent] != "" {
				t.Fatal("native OAuth login blocked by gateway config", request.NativeProblems)
			}
			for name, encoded := range request.Files {
				if strings.Contains(name, "credentials") || name == "codex/auth.json" {
					continue
				}
				data, _ := base64.StdEncoding.DecodeString(encoded)
				if strings.Contains(string(data), "localhost") || strings.Contains(string(data), "127.0.0.1") || strings.Contains(string(data), "proxy-only-key") {
					t.Fatalf("gateway override retained in %s", name)
				}
			}
			result, err := ApplyNativeAccounts(context.Background(), remote, request)
			if err != nil || !result.NativeReady[agent] {
				t.Fatal("native identity not ready", err)
			}
			raw, _ := os.ReadFile(filepath.Join(home, path))
			if string(raw) != original {
				t.Fatal("local config modified")
			}
		})
	}
}
func TestNativeOAuthDoesNotReinterpretProxyAPIIdentity(t *testing.T) {
	for _, auth := range []string{`{"OPENAI_API_KEY":"proxy"}`, `{"auth_mode":"apikey","OPENAI_API_KEY":"proxy","tokens":{"access_token":"stale"}}`} {
		if nativeOAuthIdentity("codex", func(string) []byte { return []byte(auth) }) {
			t.Fatal("API key identity reinterpreted")
		}
	}
}
func TestNativeLaunchIsolatesOAuthFromOtherAgentsEnvironment(t *testing.T) {
	home := t.TempDir()
	root := ".local/fennec/native/"
	nativeFixture(t, home, root+"env.json", `{"OPENAI_API_KEY":"pi-key","OPENAI_BASE_URL":"https://proxy.example","ANTHROPIC_API_KEY":"pi-key","ANTHROPIC_AUTH_TOKEN":"proxy-token","ANTHROPIC_BASE_URL":"https://proxy.example","GEMINI_API_KEY":"pi-google"}`)
	nativeFixture(t, home, root+"codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"oauth"}}`)
	nativeFixture(t, home, root+"claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"oauth"}}`)
	t.Setenv("ANTHROPIC_MODEL", "foreign-model")
	for _, agent := range []string{"codex", "claude"} {
		env, err := nativeAgentEnvironment(home, agent)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range env {
			if strings.Contains(entry, "pi-key") || strings.Contains(entry, "proxy.example") || strings.Contains(entry, "proxy-token") || strings.Contains(entry, "pi-google") || strings.Contains(entry, "foreign-model") {
				t.Fatal("another agent or inherited override crossed native identity boundary")
			}
		}
	}
	pi, err := nativeAgentEnvironment(home, "pi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(pi, "\n"), "OPENAI_API_KEY=pi-key") {
		t.Fatal("Pi environment was erased")
	}
}
func TestOlderNativeEnvelopeCannotRedirectOAuth(t *testing.T) {
	home := t.TempDir()
	files := map[string]string{}
	for name, data := range map[string]string{"codex/auth.json": `{"auth_mode":"chatgpt","tokens":{"access_token":"oauth"}}`, "codex/config.toml": "model_provider='proxy'\n[model_providers.proxy]\nbase_url='https://proxy.example/v1'\n", "env.json": "{}"} {
		files[name] = base64.StdEncoding.EncodeToString([]byte(data))
	}
	if _, err := ApplyNativeAccounts(context.Background(), home, ModelAccountsRequest{Version: 1, Native: true, Files: files, Agents: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/codex/config.toml"))
	if strings.Contains(string(data), "proxy") {
		t.Fatal("old export redirected native OAuth")
	}
	auth, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/codex/auth.json"))
	var obj map[string]any
	if json.Unmarshal(auth, &obj) != nil || obj["auth_mode"] != "chatgpt" {
		t.Fatal("identity changed")
	}
}

func TestNativeOAuthPolicyUpgradePreservesCloudTUI(t *testing.T) {
	home := t.TempDir()
	root := ".local/fennec/native/"
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"oauth"}}`
	original := "cli_auth_credentials_store='file'\nmodel='gpt-original'\nmodel_provider='proxy'\n[model_providers.proxy]\nbase_url='https://proxy.example/v1'\n"
	nativeFixture(t, home, root+"codex/auth.json", auth)
	nativeFixture(t, home, root+"codex/config.toml", strings.Replace(original, "gpt-original", "gpt-cloud", 1)+"[tui]\nnotifications=true\n")
	baseline, _ := json.Marshal(map[string]credentialPair{"codex/auth.json": {Source: credentialHash([]byte(auth)), Remote: credentialHash([]byte(auth))}, "codex/config.toml": {Source: credentialHash([]byte(original)), Remote: credentialHash([]byte(original))}})
	nativeFixture(t, home, root+".baseline.json", string(baseline))
	files := map[string]string{"codex/auth.json": base64.StdEncoding.EncodeToString([]byte(auth)), "codex/config.toml": base64.StdEncoding.EncodeToString([]byte(original))}
	if _, err := ApplyNativeAccounts(context.Background(), home, ModelAccountsRequest{Version: 1, Native: true, Files: files}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, root, "codex/config.toml"))
	if strings.Contains(string(raw), "proxy") || !strings.Contains(string(raw), "gpt-cloud") || !strings.Contains(string(raw), "notifications = true") {
		t.Fatal("native policy failed to preserve independent cloud settings")
	}
}

func TestNativeClaudeOAuthEnvironmentWithoutSavedFile(t *testing.T) {
	home, remote := t.TempDir(), t.TempDir()
	nativeFixture(t, home, ".claude/settings.json", `{"env":{"ANTHROPIC_BASE_URL":"http://localhost:3425","ANTHROPIC_AUTH_TOKEN":"proxy"}}`)
	source := NativeSource{Home: home, Env: func(key string) string {
		if key == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "explicit-native-oauth"
		}
		return ""
	}}
	request, err := source.GatherAll(context.Background(), []string{"claude"})
	if err != nil || len(request.NativeProblems) != 0 {
		t.Fatal(request.NativeProblems, err)
	}
	result, err := ApplyNativeAccounts(context.Background(), remote, request)
	if err != nil || !result.NativeReady["claude"] {
		t.Fatal("explicit native OAuth token rejected", err)
	}
	env, err := nativeAgentEnvironment(remote, "claude")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CLAUDE_CODE_OAUTH_TOKEN=explicit-native-oauth") || strings.Contains(joined, "ANTHROPIC_BASE_URL=") {
		t.Fatal("environment identity did not retain native route")
	}
}

func TestNativeClaudeOAuthSettingsWithoutSavedFile(t *testing.T) {
	home, remote := t.TempDir(), t.TempDir()
	nativeFixture(t, home, ".claude/settings.json", `{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"native-settings-oauth","ANTHROPIC_BASE_URL":"http://localhost:3425","ANTHROPIC_AUTH_TOKEN":"proxy"}}`)
	request, err := (NativeSource{Home: home}).GatherAll(context.Background(), []string{"claude"})
	if err != nil || len(request.NativeProblems) != 0 {
		t.Fatal(request.NativeProblems, err)
	}
	result, err := ApplyNativeAccounts(context.Background(), remote, request)
	if err != nil || !result.NativeReady["claude"] {
		t.Fatal("native OAuth settings identity rejected", err)
	}
	raw, _ := os.ReadFile(filepath.Join(remote, ".local/fennec/native/claude/settings.json"))
	if !strings.Contains(string(raw), "native-settings-oauth") || strings.Contains(string(raw), "localhost") || strings.Contains(string(raw), "proxy") {
		t.Fatal("settings OAuth was lost or redirected")
	}
}
