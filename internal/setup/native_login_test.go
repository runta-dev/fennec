package setup

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeLoginIsolatesAndActivatesCloudAccount(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	plan := NativeLogin{ID: "login-first", Agent: "codex"}
	nativeFixture(t, home, ".local/fennec/native/codex/auth.json", `{"tokens":{"access_token":"existing-local-origin"}}`)
	if err := PrepareNativeLogin(ctx, home, plan); err != nil {
		t.Fatal(err)
	}
	args, env, err := NativeLoginCommand(home, plan)
	if err != nil || strings.Join(args, " ") != "login --device-auth" {
		t.Fatal(args, err)
	}
	if strings.Contains(strings.Join(env, "\n"), "existing-local-origin") {
		t.Fatal("credential copied into environment")
	}
	if err = ActivateNativeLogin(ctx, home, plan); err == nil {
		t.Fatal("empty login accepted")
	}
	root, selected, err := NativeAccountRoot(home, "codex")
	if err != nil || selected || root != filepath.Join(home, ".local/fennec/native") {
		t.Fatal(root, selected, err)
	}
	nativeFixture(t, home, ".local/fennec/native-logins/login-first/codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"cloud-account"}}`)
	if err = ActivateNativeLogin(ctx, home, plan); err != nil {
		t.Fatal(err)
	}
	req := ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex"}, NativeProblems: map[string]string{"codex": "local_provider_unavailable"}, Files: map[string]string{"codex/auth.json": base64.StdEncoding.EncodeToString([]byte(`{"tokens":{"access_token":"changed-local-account"}}`))}}
	result, err := ApplyNativeAccounts(ctx, home, req)
	if err != nil || !result.NativeReady["codex"] {
		t.Fatal(result.NativeReady, err)
	}
	root, selected, err = NativeAccountRoot(home, "codex")
	if err != nil || !selected {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "codex/auth.json"))
	if !strings.Contains(string(raw), "cloud-account") {
		t.Fatal("local sync overwrote selected cloud login")
	}
	env, err = nativeAgentEnvironment(home, "codex")
	if err != nil || !strings.Contains(strings.Join(env, "\n"), "CODEX_HOME="+filepath.Join(root, "codex")) {
		t.Fatal("launcher missed selected account", err)
	}
}
func TestNativeLoginLateAndCanceledAttemptsCannotSelectIdentity(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	first := NativeLogin{ID: "login-first", Agent: "codex"}
	second := NativeLogin{ID: "login-second", Agent: "codex"}
	for _, plan := range []NativeLogin{first, second} {
		if err := PrepareNativeLogin(ctx, home, plan); err != nil {
			t.Fatal(err)
		}
		nativeFixture(t, home, ".local/fennec/native-logins/"+plan.ID+"/codex/auth.json", `{"tokens":{"access_token":"fixture"}}`)
	}
	if err := ActivateNativeLogin(ctx, home, first); err == nil {
		t.Fatal("late login replaced newer attempt")
	}
	if err := CancelNativeLogin(ctx, home, second); err != nil {
		t.Fatal(err)
	}
	if err := ActivateNativeLogin(ctx, home, second); err == nil {
		t.Fatal("canceled login activated")
	}
	if err := CancelNativeLogin(ctx, home, NativeLogin{ID: "normal-session", Agent: "codex"}); err == nil {
		t.Fatal("ordinary session treated as login")
	}
}
func TestNativeLoginInvalidSelectionNeverFallsBack(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".local/fennec/native/.cloud-logins.json", `{"codex":"../../escape"}`)
	if _, _, err := NativeAccountRoot(home, "codex"); err == nil {
		t.Fatal("invalid selection silently fell back")
	}
}
func TestNativeLoginRunsWithIsolatedEnvironment(t *testing.T) {
	fixture := t.TempDir()
	source := filepath.Join(fixture, "agent.go")
	code := `package main
import("os";"path/filepath")
func main(){if os.Getenv("OPENAI_API_KEY")!=""{os.Exit(3)};h:=os.Getenv("CODEX_HOME");if h==""{os.Exit(4)};if err:=os.WriteFile(filepath.Join(h,"auth.json"),[]byte("{\"auth_mode\":\"chatgpt\",\"tokens\":{\"access_token\":\"fixture\"}}"),0600);err!=nil{os.Exit(5)}}`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if raw, err := exec.Command("go", "build", "-o", filepath.Join(fixture, "codex"), source).CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, raw)
	}
	t.Setenv("PATH", fixture)
	t.Setenv("OPENAI_API_KEY", "must-not-be-forwarded")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/custom/egress-ca.crt")
	t.Setenv("SSL_CERT_FILE", "/custom/ca-bundle.crt")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".pi/agent/bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".pi/agent/bin/fd"), []byte("#!/bin/sh\necho fd 10.2.0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	plan := NativeLogin{ID: "login-test", Agent: "codex"}
	if err := PrepareNativeLogin(context.Background(), home, plan); err != nil {
		t.Fatal(err)
	}
	_, loginEnv, err := NativeLoginCommand(home, plan)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(loginEnv, "\n") + "\n"
	for _, required := range []string{"\nNODE_EXTRA_CA_CERTS=/custom/egress-ca.crt\n", "\nSSL_CERT_FILE=/custom/ca-bundle.crt\n"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing system TLS variable %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, "OPENAI_API_KEY=") {
		t.Fatalf("provider API key leaked into native login environment: %q", joined)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/bin/fd")); err != nil {
		t.Fatalf("expected ~/.local/bin/fd symlink to be created: %v", err)
	}
	if err := RunNativeLogin(context.Background(), home, plan, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, selected, err := NativeAccountRoot(home, "codex"); err != nil || !selected {
		t.Fatal(selected, err)
	}
}

func TestNativeCloudPreferenceIsOnlyAnAccountScopedHint(t *testing.T) {
	dir := t.TempDir()
	if err := SaveNativeCloudPreference(dir, "first-user", "first-runtime", "codex", "login-fixture"); err != nil {
		t.Fatal(err)
	}
	if !NativeCloudPreferences(dir, "first-user", "first-runtime")["codex"] {
		t.Fatal("preference missing")
	}
	if len(NativeCloudPreferences(dir, "other-user", "first-runtime")) != 0 || len(NativeCloudPreferences(dir, "first-user", "other-runtime")) != 0 {
		t.Fatal("account preference crossed ownership scope")
	}
}
func TestNativeLoginCannotActivateSymlinkedCredentialDirectory(t *testing.T) {
	home := t.TempDir()
	plan := NativeLogin{ID: "login-fixture", Agent: "codex"}
	ctx := context.Background()
	if err := PrepareNativeLogin(ctx, home, plan); err != nil {
		t.Fatal(err)
	}
	root := nativeLoginDirectory(home, plan.ID)
	other := t.TempDir()
	nativeFixture(t, other, "auth.json", `{"tokens":{"access_token":"fixture"}}`)
	if err := os.RemoveAll(filepath.Join(root, "codex")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "codex")); err != nil {
		t.Fatal(err)
	}
	if err := ActivateNativeLogin(ctx, home, plan); err == nil {
		t.Fatal("symlinked credentials accepted")
	}
}

func TestCanceledLoginDiscardsSecretsButNotActiveAccount(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	plan := NativeLogin{ID: "login-fixture", Agent: "codex"}
	if err := PrepareNativeLogin(ctx, home, plan); err != nil {
		t.Fatal(err)
	}
	nativeFixture(t, home, ".local/fennec/native-logins/login-fixture/codex/auth.json", `{"tokens":{"access_token":"cancel-me"}}`)
	if err := DiscardNativeLogin(ctx, home, plan); err == nil {
		t.Fatal("pending login was discarded")
	}
	if err := CancelNativeLogin(ctx, home, plan); err != nil {
		t.Fatal(err)
	}
	if err := DiscardNativeLogin(ctx, home, plan); err != nil {
		t.Fatal(err)
	}
	if err := CancelNativeLogin(ctx, home, plan); err != nil {
		t.Fatal("cancellation lost idempotency", err)
	}
	entries, err := os.ReadDir(nativeLoginDirectory(home, plan.ID))
	if err != nil || len(entries) != 1 || entries[0].Name() != "login.json" {
		t.Fatal("canceled secrets retained", err)
	}
}
