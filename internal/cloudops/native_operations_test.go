package cloudops

import (
	"context"
	"encoding/json"
	"fennec/internal/runtime"
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
	r := runtime.LocalRuntime{Home: home}
	ctx := context.Background()
	raw, err := Operate(ctx, &r, Operation{Kind: "native_models", NativeModel: &setup.NativeModelSelection{Agent: "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	var state setup.NativeModelState
	if json.Unmarshal(raw, &state) != nil || len(state.Models) != 2 {
		t.Fatal(string(raw))
	}
	for _, id := range []string{"kimi-code/k3", "gpt-not-in-catalog"} {
		if _, err = Operate(ctx, &r, Operation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex", Model: id}}); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if _, err = Operate(ctx, &r, Operation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex", Model: "gpt-test"}}); err != nil {
		t.Fatal(err)
	}
	if err = setup.ConfigureAgentMode(home, false); err != nil {
		t.Fatal(err)
	}
	if _, err = Operate(ctx, &r, Operation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex"}}); err == nil {
		t.Fatal("native mutation permitted after gateway toggle")
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
	backend := runtime.LocalRuntime{Home: home, Query: func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"terminals":[]}`), nil
	}}
	raw, err := List(context.Background(), &backend)
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

func TestNativeLoginStatusRequiresConfirmedTerminalState(t *testing.T) {
	for _, state := range []string{"live", "finished", "", "unknown", "reconnecting"} {
		t.Run(state, func(t *testing.T) {
			r := runtime.LocalRuntime{Home: t.TempDir(), Run: func(context.Context, ...string) ([]byte, error) {
				return json.Marshal(runtime.LocalTerminal{Address: "t1", State: state, Labels: map[string]string{"fennec.session": "login-fixture", "fennec.agent": "codex"}})
			}}
			raw, err := Operate(context.Background(), &r, Operation{Kind: "native_login_status", Address: "t1", Login: &setup.NativeLogin{ID: "login-fixture", Agent: "codex"}})
			if state != "live" && state != "finished" {
				if err == nil {
					t.Fatal("unknown terminal state could start another login", string(raw))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Running  bool `json:"running"`
				SignedIn bool `json:"signed_in"`
			}
			if err = json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.Running != (state == "live") || result.SignedIn {
				t.Fatal(string(raw))
			}
		})
	}
}
