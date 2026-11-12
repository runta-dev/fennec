package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDefaultsAndOverrides(t *testing.T) {
	d := Defaults()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.BaseURL != DefaultBaseURL || d.DecisionsURL != DefaultDecisionsURL || d.APIKeyEnv != "OPENROUTER_API_KEY" ||
		d.TimeoutMS != 60000 || d.DecideAPI != DecideDecisions || d.DecideBuild != nil || d.Models.Verify != nil {
		t.Errorf("defaults = %+v", d)
	}
	c, err := ParseConfigJSON([]byte(`{"base_url": "http://localhost:8000/v1", "decide_api": "chat", "models": {"ask": "local"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://localhost:8000/v1" || c.DecideAPI != DecideChat || c.Models.Ask != "local" ||
		c.Models.Decide != DefaultDecideModel || c.TimeoutMS != DefaultTimeoutMS {
		t.Errorf("overrides = %+v", c)
	}
	c, err = ConfigFromMap(map[string]any{"timeout_ms": 5000, "decide_build": "jev-########", "models": map[string]any{"verify": "v"}})
	if err != nil || c.TimeoutMS != 5000 || *c.DecideBuild != "jev-########" || *c.Models.Verify != "v" {
		t.Errorf("from map = %+v, %v", c, err)
	}
}

func TestConfigStrictValidation(t *testing.T) {
	cases := map[string]string{
		`{"base_url": "ftp://x"}`:                    "base_url",
		`{"base_url": ""}`:                           "base_url",
		`{"decisions_url": "not a url"}`:             "decisions_url",
		`{"timeout_ms": 0}`:                          "timeout_ms",
		`{"timeout_ms": "60"}`:                       "timeout_ms",
		`{"decide_api": "grpc"}`:                     "grpc",
		`{"decide_build": ""}`:                       "decide_build",
		`{"models": {"decide": ""}}`:                 "models.decide",
		`{"models": {"verify": ""}}`:                 "models.verify",
		`{"models": {"bogus": "x"}}`:                 "bogus",
		`{"apikey": "sk-should-not-print"}`:          "apikey",
		`{"api_key_env": "A=B"}`:                     "api_key_env",
		`{"base_url": "http://u:sk-pw@h/v1", "x":1}`: "x",
	}
	for in, want := range cases {
		_, err := ParseConfigJSON([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to name %s", in, err, want)
			continue
		}
		if strings.Contains(err.Error(), "sk-") {
			t.Errorf("%s: error echoes a secret: %v", in, err)
		}
	}
	// Chat mode needs no decisions URL.
	if _, err := ParseConfigJSON([]byte(`{"decide_api": "chat", "decisions_url": ""}`)); err != nil {
		t.Errorf("chat without decisions_url: %v", err)
	}
	if _, err := New(Config{}); err == nil {
		t.Error("New took an empty configuration")
	}
}

func TestKeyFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	noKey := Defaults()
	if err := noKey.CheckKeyFile(path); err != nil {
		t.Errorf("a file without a key may be readable: %v", err)
	}
	withKey := Defaults()
	withKey.APIKey = "sk-file"
	err := withKey.CheckKeyFile(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600 "+path) || strings.Contains(err.Error(), "sk-file") {
		t.Errorf("open file with a key: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := withKey.CheckKeyFile(path); err != nil {
		t.Errorf("0600 refused: %v", err)
	}
	if err := withKey.CheckKeyFileMode(path, 0o640); err == nil {
		t.Error("group-readable file accepted")
	}
}
