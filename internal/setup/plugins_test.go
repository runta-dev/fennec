package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesiredPluginsUsesInstalledVersion(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config/magpie")
	pkg := filepath.Join(root, "plugins/node_modules/@magpie-community/opencode-devin-auth")
	os.MkdirAll(pkg, 0700)
	os.WriteFile(filepath.Join(root, "plugins.json"), []byte(`{"plugins":[{"spec":"@magpie-community/opencode-devin-auth@latest"},{"spec":"unrelated@latest"}]}`), 0600)
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@magpie-community/opencode-devin-auth","version":"0.1.37"}`), 0600)
	got, err := DesiredModelPlugins(home)
	if err != nil || len(got) != 1 || got[0].Version != "0.1.37" {
		t.Fatalf("%+v %v", got, err)
	}
	os.Remove(filepath.Join(pkg, "package.json"))
	if _, err = DesiredModelPlugins(home); err == nil {
		t.Fatal("missing installation silently accepted")
	}
}
