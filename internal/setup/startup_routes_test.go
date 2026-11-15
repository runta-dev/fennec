package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupRoutesNeverExecuteMagpieAndPreserveSavedChoices(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "launched")
	if err := os.WriteFile(filepath.Join(bin, "magpie"), []byte("#!/bin/sh\n/usr/bin/touch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("KIMI_CODE_HOME", filepath.Join(home, ".kimi-code"))
	routes := ConfiguredAgentRoutes(home, []Agent{{ID: "kimi"}})
	if len(routes) != 1 || routes[0].Problem != "model_selection_required" {
		t.Fatalf("%+v", routes)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("startup executed a model-query subprocess")
	}
	directory := filepath.Join(home, ".config/fennec")
	if err := SaveModelPreference(directory, "owner", "kimi", "kimi-code/k3"); err != nil {
		t.Fatal(err)
	}
	routes, err := ApplyModelPreferences(directory, "owner", routes)
	if err != nil || routes[0].Model != "kimi-code/k3" || routes[0].Problem != "" {
		t.Fatalf("%+v %v", routes, err)
	}
}

func TestKimiConfiguredMagpieRouteRequiresLocalGateway(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".kimi-code")
	t.Setenv("KIMI_CODE_HOME", root)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		url   string
		valid bool
	}{{"http://127.0.0.1:3425/v1", true}, {"https://api.moonshot.cn/v1", false}} {
		data := "default_model = 'magpie/selected'\n[models.'magpie/selected']\nprovider = 'gateway'\nmodel = 'kimi-code/k3'\n[providers.gateway]\nbase_url = '" + test.url + "'\n"
		if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		model, ok := readConfiguredMagpieModel(home, "kimi")
		if ok != test.valid || (ok && model != "kimi-code/k3") {
			t.Fatalf("model=%q valid=%v", model, ok)
		}
	}
}
