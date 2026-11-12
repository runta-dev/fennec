package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/sched"
)

func writeConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.APIKeyEnv != "OPENROUTER_API_KEY" || cfg.Model.TimeoutMS != 60000 || cfg.Model.DecideBuild != nil || cfg.Model.Models.Verify != nil {
		t.Errorf("model defaults %+v", cfg.Model)
	}
	if !strings.Contains(cfg.Model.BaseURL, "openrouter") {
		t.Errorf("base url %q", cfg.Model.BaseURL)
	}
	// A file overrides only what it names.
	path := writeConfig(t, "[model]\ntimeout_ms = 5000\n", 0o644)
	cfg, err = LoadConfig(path)
	if err != nil || cfg.Model.TimeoutMS != 5000 || cfg.Model.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Errorf("override %+v %v", cfg.Model, err)
	}
}

func TestConfigStrict(t *testing.T) {
	cases := map[string]string{
		"[model]\nfoo = 1\n":                          "line 2: unknown key model.foo",
		"[model]\ntimeout_ms = \"soon\"\n":            "wrong type",
		"[model]\ndecide_api = \"magic\"\n":           "[model]",
		"[wat]\nx = 1\n":                              "unknown key wat",
		"[model]\napi_kee = \"sk-very-secret-123\"\n": "unknown key model.api_kee",
		"[desk]\ninput = \"sideways\"\n":              "config.toml:2: [desk] input must be",
		"[safety]\nconfirm = \"sometimes\"\n":         "[safety] confirm must be",
		"[desk]\nmode = \"auto\"\n":                   "unknown key desk.mode",
		"[safety]\nuser_idle = 3\n":                   "unknown key safety.user_idle",
		"[driver]\ncursor = true\n":                   "unknown key driver.cursor",
		"[driver]\noverlay = \"yes\"\n":               "wrong type",
		"[daemon]\nsocket_mode = \"0666\"\n":          "unknown key daemon.socket_mode",
	}
	for body, want := range cases {
		_, err := LoadConfig(writeConfig(t, body, 0o644))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (want %q)", body, err, want)
			continue
		}
		if strings.Contains(err.Error(), "sk-very-secret") {
			t.Errorf("%q: the error echoes the line: %v", body, err)
		}
	}
}

func TestConfigKey(t *testing.T) {
	t.Setenv("CUA_TEST_KEY", "from-env")
	cfg, err := LoadConfig(writeConfig(t, "[model]\napi_key_env = \"CUA_TEST_KEY\"\n", 0o644))
	if err != nil {
		t.Fatal(err)
	}
	if k := cfg.Model.Key(os.Getenv); k.Reveal() != "from-env" {
		t.Errorf("env key %q", k.Reveal())
	}
	cfg, err = LoadConfig(writeConfig(t, "[model]\napi_key = \"from-file\"\napi_key_env = \"CUA_TEST_KEY\"\n", 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if k := cfg.Model.Key(os.Getenv); k.Reveal() != "from-file" {
		t.Errorf("file key %q", k.Reveal())
	}
	data, _ := json.Marshal(cfg.Model)
	if strings.Contains(string(data), "from-file") {
		t.Errorf("the key marshals: %s", data)
	}
}

func TestConfigKeyFileMode(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "[model]\napi_key = \"sk-x\"\n", 0o644))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") || strings.Contains(err.Error(), "sk-x") {
		t.Errorf("readable key file: %v", err)
	}
}

func TestConfigSafetyTables(t *testing.T) {
	cfg, _ := LoadConfig(filepath.Join(t.TempDir(), "none.toml"))
	if len(cfg.Safety.Allow) != 0 || cfg.Safety.KillHotkey != "ctrl+alt+cmd+escape" || !cfg.Safety.Indicator || cfg.Safety.Confirm != "off" || !cfg.Tools.Run || cfg.Tools.Plugins != "" || !cfg.Driver.Overlay || cfg.Desk.Input != "foreground" || cfg.Desk.LockedInput {
		t.Errorf("defaults %+v", cfg)
	}
	cfg, err := LoadConfig(writeConfig(t, `[safety]
allow = ["TextEdit", "Safari"]
kill_hotkey = "off"
indicator = false
confirm = "every"
[tools]
run = false
plugins = "/opt/cua/tools.d"
[driver]
overlay = false
[desk]
input = "auto"
locked_input = true
`, 0o644))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Safety.Allow) != 2 || cfg.Safety.KillHotkey != "off" || cfg.Safety.Indicator || cfg.Safety.Confirm != "every" || cfg.Tools.Run || cfg.Tools.Plugins != "/opt/cua/tools.d" || cfg.Driver.Overlay || cfg.Desk.Input != "auto" || !cfg.Desk.LockedInput {
		t.Errorf("set %+v", cfg)
	}
}

func TestConfigSocketGroup(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, "[daemon]\nsocket_group = \"staff\"\n", 0o644))
	if err != nil || cfg.Daemon.SocketGroup != "staff" {
		t.Errorf("%+v %v", cfg.Daemon, err)
	}
	if _, err := LoadConfig(writeConfig(t, "[daemon]\nsocket_group = \"\"\n", 0o644)); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Errorf("empty group: %v", err)
	}
}

func TestBrokenConfigFailsClosed(t *testing.T) {
	_, loadErr := LoadConfig(writeConfig(t, "[desk]\ninput = \"sideways\"\n", 0o644))
	eng := &engine.Engine{Turn: &sched.Turn{}, Stopper: &sched.Stopper{}, Broken: loadErr}
	for _, tool := range []string{"observe", "act", "wait", "run", "flow"} {
		r := eng.Call(context.Background(), &mcpserver.Session{AgentID: "a"}, tool, nil)
		if !r.IsError || !strings.Contains(r.Text, "config.toml:2") {
			t.Errorf("%s with a broken config: %s", tool, r.Text)
		}
	}
	// Stop and resume still work.
	eng.Stopper.Stop()
	if !eng.Stopper.Stopped() {
		t.Error("stop failed")
	}
	eng.Stopper.Resume()
}
