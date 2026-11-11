package runtime

import (
	"context"
	"encoding/json"
	"fennec/internal/setup"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeModelOperationsUseNativeCatalog(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local/fennec/native/codex")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-test"},{"slug":"kimi-code/k3"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := LocalRuntime{Home: home}
	ctx := context.Background()
	raw, err := r.Operation(ctx, LocalOperation{Kind: "native_models", NativeModel: &setup.NativeModelSelection{Agent: "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	var state setup.NativeModelState
	if json.Unmarshal(raw, &state) != nil || len(state.Models) != 2 {
		t.Fatal(string(raw))
	}
	for _, id := range []string{"kimi-code/k3", "gpt-not-in-catalog"} {
		if _, err = r.Operation(ctx, LocalOperation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex", Model: id}}); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if _, err = r.Operation(ctx, LocalOperation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex", Model: "gpt-test"}}); err != nil {
		t.Fatal(err)
	}
	if err = setup.ConfigureAgentMode(home, false); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Operation(ctx, LocalOperation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex"}}); err == nil {
		t.Fatal("native mutation permitted after gateway toggle")
	}
}

func TestNativeLoginLaunchHasNoModelPromptOrHooks(t *testing.T) {
	home := t.TempDir()
	var commands [][]string
	backend := LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/fixture/codex", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		commands = append(commands, append([]string{}, args...))
		return []byte(`{"address":"t1","state":"live"}`), nil
	}}
	request := LocalLaunch{ID: "login-fixture", Agent: "codex", Device: "1234abcd", LocalDirectory: "/", NativeLogin: true}
	if _, err := backend.Launch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 {
		t.Fatal("login was not launched exactly once")
	}
	args := commands[0]
	found := false
	for i, arg := range args {
		if arg == "native-login" {
			found = i+2 < len(args) && args[i+1] == "codex" && args[i+2] == "login-fixture"
		}
		if arg == "--no-daemon" || arg == "--permission-mode" {
			t.Fatal("regular agent options entered authentication")
		}
	}
	if !found {
		t.Fatal("dedicated native login command missing")
	}
	request.Arguments = []string{"untrusted prompt"}
	if request.Validate() == nil {
		t.Fatal("login accepted user-supplied command arguments")
	}
}

func TestResidentListingCarriesOnlyPublicLoginIdentities(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local/fennec/native")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cloud-logins.json"), []byte(`{"codex":"login-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	backend := LocalRuntime{Home: home, Run: func(context.Context, ...string) ([]byte, error) { return []byte(`{"terminals":[]}`), nil }}
	raw, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Accounts map[string]string `json:"native_accounts"`
	}
	if json.Unmarshal(raw, &listing) != nil || listing.Accounts["codex"] != "login-fixture" {
		t.Fatal("completion missing from resident listing")
	}
}
