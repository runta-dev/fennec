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
