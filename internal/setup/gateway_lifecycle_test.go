package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCredentialCopyDoesNotToggleGatewayMode(t *testing.T) {
	home := t.TempDir()
	if err := ConfigureAgentMode(home, false); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNativeAccounts(context.Background(), home, ModelAccountsRequest{Version: 1, Native: true}, false); err != nil {
		t.Fatal(err)
	}
	if NativeAgentMode(home) {
		t.Fatal("credential copy changed routing mode")
	}
	if _, err := ApplyNativeAccounts(context.Background(), home, ModelAccountsRequest{Version: 1, Native: true}); err != nil {
		t.Fatal(err)
	}
	if !NativeAgentMode(home) {
		t.Fatal("explicit native switch was lost")
	}
}
func TestPluginNormalizationCommitsBeforeRestartFailure(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	os.MkdirAll(root, 0700)
	os.WriteFile(filepath.Join(root, "plugins.json"), []byte(`{"plugins":[{"spec":"@magpie-community/test@1.2.3"}]}`), 0600)
	path := filepath.Join(root, "plugin-providers.json")
	os.WriteFile(path, []byte(`[{"spec":"@magpie-community/test@latest","id":"test"}]`), 0600)
	if err := alignPluginProviderSpecsAndReload(context.Background(), home); err == nil {
		t.Fatal("unmanaged service restart reported success")
	}
	b, _ := os.ReadFile(path)
	if string(b) != `[{"id":"test","spec":"@magpie-community/test@1.2.3"}]` {
		t.Fatal(string(b))
	}
	// No content change means no service operation, even with no installed gateway.
	if err := alignPluginProviderSpecsAndReload(context.Background(), home); err != nil {
		t.Fatal(err)
	}
}
