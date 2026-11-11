package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestModelIdentityReconcilePreservesExistingLogin(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0700)
	auth := filepath.Join(home, ".codex/auth.json")
	os.WriteFile(auth, []byte(`{"existing":true}`), 0600)
	if err := restoreLocalCodexIdentity(context.Background(), home, func(context.Context, ...string) error { t.Fatal("existing identity switched"); return nil }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(auth)
	if string(data) != `{"existing":true}` {
		t.Fatal("overwrote existing identity")
	}
}

func TestIdentityRestoreSelectsOnlyOneEligibleAccount(t *testing.T) {
	for _, mode := range []string{"single", "ambiguous", "hidden", "disabled", "local-home", "missing-output"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			entry := map[string]any{"agent": "codex", "on": true, "user": "fixture-user", "auth": map[string]string{"token": "fixture"}}
			switch mode {
			case "hidden":
				entry["hidden"] = true
			case "disabled":
				entry["on"] = false
			case "local-home":
				entry["home"] = "/local-only"
			}
			entries := []map[string]any{entry}
			if mode == "ambiguous" {
				entries = append(entries, map[string]any{"agent": "codex", "on": true, "user": "second-user", "auth": "fixture"})
			}
			root := filepath.Join(home, ".config/magpie")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(entries)
			if err := os.WriteFile(filepath.Join(root, "logins.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := restoreLocalCodexIdentity(context.Background(), home, func(_ context.Context, args ...string) error {
				calls++
				if !reflect.DeepEqual(args, []string{"accounts", "switch", "codex", "fixture-user"}) {
					t.Fatal(args)
				}
				if mode == "missing-output" {
					return nil
				}
				if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(home, ".codex/auth.json"), []byte(`{"restored":true}`), 0600)
			})
			if (err != nil) != (mode == "missing-output") {
				t.Fatalf("unexpected restore result: %v", err)
			}
			want := 0
			if mode == "single" || mode == "missing-output" {
				want = 1
			}
			if calls != want {
				t.Fatalf("unexpected identity switching: %d", calls)
			}
		})
	}
}
