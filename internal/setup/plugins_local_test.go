package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocalPluginReconcileVerifiesAndReusesInstallation(t *testing.T) {
	home := t.TempDir()
	plugin := ModelPlugin{Package: "@magpie-community/opencode-devin-auth", Version: "0.1.37"}
	root := filepath.Join(home, ".config/magpie")
	manifest := filepath.Join(root, "plugins/node_modules", plugin.Package, "package.json")
	calls := 0
	run := func(_ context.Context, args ...string) error {
		calls++
		if !reflect.DeepEqual(args, []string{"plugin", "add", plugin.Package + "@" + plugin.Version}) {
			t.Fatal(args)
		}
		if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"plugins": []map[string]string{{"spec": plugin.Package + "@" + plugin.Version}}})
		if err := os.WriteFile(filepath.Join(root, "plugins.json"), data, 0600); err != nil {
			return err
		}
		data, _ = json.Marshal(map[string]string{"name": plugin.Package, "version": plugin.Version})
		return os.WriteFile(manifest, data, 0600)
	}
	for i := 0; i < 2; i++ {
		if err := reconcileLocalModelPlugins(context.Background(), home, []ModelPlugin{plugin}, run); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("existing plugin reinstalled", calls)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if err := reconcileLocalModelPlugins(context.Background(), home, []ModelPlugin{plugin}, func(context.Context, ...string) error { return nil }); err == nil {
		t.Fatal("missing artifact accepted after nominal install success")
	}
	plugin.Package = "unapproved-plugin"
	if err := reconcileLocalModelPlugins(context.Background(), home, []ModelPlugin{plugin}, run); err == nil {
		t.Fatal("unapproved package accepted")
	}
	if calls != 1 {
		t.Fatal("unapproved package reached installer")
	}
}

func TestEmptyPluginSetNeedsNoRemoteConnection(t *testing.T) {
	if err := ReconcileModelPlugins(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
}
