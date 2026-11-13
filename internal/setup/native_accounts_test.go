package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"

	"path/filepath"
	"strings"
	"testing"
)

func nativeFixture(t *testing.T, home, path, value string) {
	t.Helper()
	p := filepath.Join(home, path)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(value), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestNativeOnboardingWithoutMagpie(t *testing.T) {
	home, remote := t.TempDir(), t.TempDir()
	nativeFixture(t, home, ".codex/auth.json", `{"tokens":{"access_token":"codex-fixture","refresh_token":"refresh"}}`)
	nativeFixture(t, home, ".codex/config.toml", "model = 'gpt-fixture'\n[mcp_servers.unsafe]\ncommand='local-only'\n")
	nativeFixture(t, home, ".pi/agent/auth.json", `{"anthropic":{"type":"api_key","key":"pi-fixture"}}`)
	nativeFixture(t, home, ".pi/agent/settings.json", `{"defaultModel":"pi-model","defaultProvider":"anthropic","extensions":["local-only"]}`)
	source := NativeSource{Home: home, Keychain: func(_ context.Context, service, account string) ([]byte, error) {
		if service != "Claude Code-credentials" {
			t.Fatal(service)
		}
		return []byte(`{"claudeAiOauth":{"accessToken":"claude-fixture"}}`), nil
	}}
	request, e := source.Gather(context.Background(), []string{"codex", "claude", "pi"})
	if e != nil {
		t.Fatal(e)
	}
	if !request.Native || len(request.Plugins) != 0 {
		t.Fatal("native flow requested gateway")
	}
	result, e := ApplyModelAccounts(context.Background(), remote, request)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range request.Agents {
		if !result.NativeReady[id] {
			t.Fatalf("%s not ready", id)
		}
	}
	if result.CatalogAvailable || result.CatalogDeferred {
		t.Fatal("native flow queried model gateway")
	}
	for _, file := range []string{".config/magpie/providers.json", ".config/magpie/logins.json"} {
		if _, e := os.Stat(filepath.Join(remote, file)); !os.IsNotExist(e) {
			t.Fatal("magpie file created")
		}
	}
	raw, e := os.ReadFile(filepath.Join(remote, ".local/fennec/native/codex/config.toml"))
	if e != nil || strings.Contains(string(raw), "mcp_servers") || !strings.Contains(string(raw), "gpt-fixture") {
		t.Fatal("model configuration lost or local hooks copied", e)
	}
	raw, _ = os.ReadFile(filepath.Join(remote, ".local/fennec/native/pi/settings.json"))
	if strings.Contains(string(raw), "extensions") {
		t.Fatal("local extension copied")
	}
	if !NativeAgentMode(remote) {
		t.Fatal("native mode not selected")
	}
}
func TestNativeRefreshAndConflictsPreserveCloudCredential(t *testing.T) {
	home, remote := t.TempDir(), t.TempDir()
	path := ".codex/auth.json"
	nativeFixture(t, home, path, `{"tokens":{"access_token":"local-one"}}`)
	source := NativeSource{Home: home}
	req, e := source.Gather(context.Background(), []string{"codex"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ApplyNativeAccounts(context.Background(), remote, req); e != nil {
		t.Fatal(e)
	}
	target := ".local/fennec/native/codex/auth.json"
	refreshed := `{"tokens":{"access_token":"cloud-refreshed"}}`
	nativeFixture(t, remote, target, refreshed)
	if _, e = ApplyNativeAccounts(context.Background(), remote, req); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(remote, target))
	if string(b) != refreshed {
		t.Fatal("stale local auth overwrote refresh")
	}
	req.Files["codex/auth.json"] = base64.StdEncoding.EncodeToString([]byte(`{"tokens":{"access_token":"other-local"}}`))
	// Neither copy is confirmed by the provider: preparation continues and
	// both computers keep their own copy.
	stubCredentialChecks(t)
	result, e := ApplyNativeAccounts(context.Background(), remote, req)
	if e != nil || len(result.NativeDeferred) != 1 {
		t.Fatalf("conflict: %v %v", e, result.NativeDeferred)
	}
	b, _ = os.ReadFile(filepath.Join(remote, target))
	if string(b) != refreshed {
		t.Fatal("cloud credential overwritten")
	}
}
func TestNativeRejectsArbitraryPathsAndInvalidEnvelopes(t *testing.T) {
	for _, name := range []string{"../../outside", "codex/../../outside", "unknown.json"} {
		r := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{name: base64.StdEncoding.EncodeToString([]byte(`{}`))}}
		if _, e := ApplyNativeAccounts(context.Background(), t.TempDir(), r); e == nil {
			t.Fatal("unsafe file accepted")
		}
	}
}
func TestNativeCodexKeychainUsesConfiguredHomeAndFileBackendRemotely(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "custom")
	nativeFixture(t, dir, "config.toml", "cli_auth_credentials_store = 'keyring'\nmodel = 'custom-model'\n")
	calls := 0
	s := NativeSource{Home: home, Env: func(k string) string {
		if k == "CODEX_HOME" {
			return dir
		}
		return ""
	}, Keychain: func(_ context.Context, service, account string) ([]byte, error) {
		calls++
		if service != "Codex Auth" || !strings.HasPrefix(account, "cli|") || len(account) != 20 {
			t.Fatal("wrong keychain identity")
		}
		return []byte(`{"tokens":{"access_token":"keychain-fixture"}}`), nil
	}}
	r, e := s.Gather(context.Background(), []string{"codex"})
	if e != nil || calls != 1 {
		t.Fatal(e, calls)
	}
	b, _ := base64.StdEncoding.DecodeString(r.Files["codex/config.toml"])
	if strings.Contains(string(b), "keyring") || !strings.Contains(string(b), "file") {
		t.Fatal("cloud keyring required")
	}
}
func TestNativeNoCredentialsIsNotReady(t *testing.T) {
	r, e := (NativeSource{Home: t.TempDir()}).Gather(context.Background(), []string{"codex", "claude", "pi"})
	if e != nil {
		t.Fatal(e)
	}
	result, e := ApplyNativeAccounts(context.Background(), t.TempDir(), r)
	if e != nil {
		t.Fatal(e)
	}
	for id, ready := range result.NativeReady {
		if ready {
			t.Fatal("empty native config marked ready", id)
		}
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "access_token") {
		t.Fatal("credential contents in result")
	}
}

func TestNativeAgentCredentialFailureDoesNotBlockPeers(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".codex/auth.json", `{"OPENAI_API_KEY":"fixture"}`)
	s := NativeSource{Home: home, Keychain: func(context.Context, string, string) ([]byte, error) { return nil, os.ErrPermission }}
	r, e := s.GatherAll(context.Background(), []string{"claude", "codex"})
	if e != nil {
		t.Fatal(e)
	}
	if r.NativeProblems["claude"] != "credentials_unavailable" || r.NativeProblems["codex"] != "" {
		t.Fatal(r.NativeProblems)
	}
	result, e := ApplyNativeAccounts(context.Background(), t.TempDir(), r)
	if e != nil || !result.NativeReady["codex"] || result.NativeReady["claude"] {
		t.Fatal("peer readiness lost", e)
	}
}
func TestNativeRefusesSymlinkDestination(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	if e := os.Symlink(other, filepath.Join(home, ".local")); e != nil {
		t.Fatal(e)
	}
	r := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{"env.json": base64.StdEncoding.EncodeToString([]byte(`{}`))}}
	if _, e := ApplyNativeAccounts(context.Background(), home, r); e == nil {
		t.Fatal("followed symlink")
	}
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Fatal("wrote outside sandbox")
	}
}
func TestNativeLocalGatewayRequiresExplicitIntegration(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".codex/auth.json", `{"OPENAI_API_KEY":"fixture"}`)
	nativeFixture(t, home, ".codex/config.toml", "model_provider='magpie'\n[model_providers.magpie]\nbase_url='http://127.0.0.1:3425/v1'\n")
	s := NativeSource{Home: home}
	r, e := s.GatherAll(context.Background(), []string{"codex"})
	if e != nil || r.NativeProblems["codex"] != "local_provider_unavailable" {
		t.Fatal("implicit gateway dependency", e)
	}
	s.Gateway = true
	r, e = s.GatherAll(context.Background(), []string{"codex"})
	if e != nil || len(r.NativeProblems) != 0 {
		t.Fatal("explicit integration rejected", e)
	}
}
func TestNativeEnvironmentMergesPerAgentWithoutCrossBlocking(t *testing.T) {
	home := t.TempDir()
	s := NativeSource{Home: home, Env: func(k string) string {
		return map[string]string{"OPENAI_API_KEY": "openai-fixture", "ANTHROPIC_API_KEY": "claude-fixture"}[k]
	}}
	r, e := s.GatherAll(context.Background(), []string{"codex", "claude"})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := base64.StdEncoding.DecodeString(r.Files["env.json"])
	var env map[string]string
	_ = json.Unmarshal(b, &env)
	if env["OPENAI_API_KEY"] == "" || env["ANTHROPIC_API_KEY"] == "" {
		t.Fatal("one agent overwrote the other environment")
	}
}

func TestNativeExecChild(t *testing.T) {
	home := os.Getenv("FENNEC_NATIVE_EXEC_HOME")
	if home == "" {
		return
	}
	if err := ExecNativeAgent(home, "codex", []string{"--fixture"}); err != nil {
		os.Exit(2)
	}
	os.Exit(3)
}
func TestNativeExecUsesIsolatedCredentials(t *testing.T) {
	home := t.TempDir()
	s := NativeSource{Home: home, Env: func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "fixture-key"
		}
		return ""
	}}
	request, e := s.Gather(context.Background(), []string{"codex"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ApplyNativeAccounts(context.Background(), home, request); e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(home, "bin")
	if e = os.MkdirAll(bin, 0700); e != nil {
		t.Fatal(e)
	}
	script := `#!/bin/sh
set -eu
test "$CODEX_HOME" = "$FENNEC_NATIVE_EXEC_HOME/.local/fennec/native/codex"
test "$OPENAI_API_KEY" = fixture-key
test -f "$CODEX_HOME/auth.json"
test "$1" = --fixture
printf 'NATIVE_READY\n'
`
	if e = os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	command := exec.Command(executable, "-test.run=^TestNativeExecChild$")
	command.Env = append(os.Environ(), "FENNEC_NATIVE_EXEC_HOME="+home, "PATH="+bin+":/usr/bin:/bin", "OPENAI_API_KEY=wrong-inherited-key", "CODEX_HOME=/wrong-inherited-home")
	raw, e := command.CombinedOutput()
	if e != nil || string(raw) != "NATIVE_READY\n" {
		t.Fatalf("native process did not use synchronized identity: %v", e)
	}
}

func TestNativeRejectsCredentialCommands(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".pi/agent/auth.json", `{"openai":{"type":"api_key","key":"!private-local-helper"}}`)
	r, e := (NativeSource{Home: home}).GatherAll(context.Background(), []string{"pi"})
	if e != nil || r.NativeProblems["pi"] != "local_credential_command" {
		t.Fatal("local command exported", e)
	}
}

func TestNativeShellEnvironmentIgnoresStartupBannerAndUnrelatedSecrets(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "shell")
	script := `#!/bin/sh
printf 'interactive startup banner\n'
export OPENAI_API_KEY=fixture-key
export UNRELATED_SECRET=not-an-agent-credential
exec /bin/sh -c "$2"
`
	if err := os.WriteFile(shell, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("OPENAI_API_KEY", "")
	env := nativeSourceEnvironment(context.Background())
	if env["OPENAI_API_KEY"] != "fixture-key" {
		t.Fatal("startup banner corrupted the first credential")
	}
	if _, ok := env["UNRELATED_SECRET"]; ok {
		t.Fatal("unrelated secret copied")
	}
}
