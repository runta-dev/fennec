package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"fennec/cua/internal/models"
)

// Config is fennec-cua's configuration file.
type Config struct {
	Model  models.Config
	Safety Safety
	Tools  Tools
	Driver Driver
	Desk   Desk
	Daemon DaemonSettings
	// Path is the file the configuration came from; empty for defaults.
	Path string
}

// Safety settings.
type Safety struct {
	// Allow lists the apps agents may see and drive; empty allows every app.
	Allow      []string
	KillHotkey string
	Indicator  bool
	Confirm    string // off, coordinate, every
}

// Tools settings.
type Tools struct {
	Run     bool
	Plugins string // plugin directory; empty means tools.d beside the file
}

// Driver settings.
type Driver struct {
	Overlay bool
}

// Desk settings.
type Desk struct {
	Input       string // foreground, background, auto
	LockedInput bool
}

// DaemonSettings are endpoint settings.
type DaemonSettings struct {
	// SocketGroup opens the agent socket to a group (mode 0660).
	SocketGroup string
}

// DefaultConfig is the configuration of a missing file.
func DefaultConfig() Config {
	return Config{
		Model:  models.Defaults(),
		Safety: Safety{KillHotkey: "ctrl+alt+cmd+escape", Indicator: true, Confirm: "off"},
		Tools:  Tools{Run: true},
		Driver: Driver{Overlay: true},
		Desk:   Desk{Input: "foreground"},
	}
}

// ConfigPath is the per-user file: FENNEC_CUA_CONFIG, else
// ~/Library/Application Support/fennec/cua/config.toml.
func ConfigPath() (string, error) {
	if p := os.Getenv("FENNEC_CUA_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fennec", "cua", "config.toml"), nil
}

// The file's shape. Pointers tell a key the file sets from one it leaves out.
type fileConfig struct {
	Model *struct {
		BaseURL      *string `toml:"base_url"`
		DecisionsURL *string `toml:"decisions_url"`
		APIKeyEnv    *string `toml:"api_key_env"`
		APIKey       *string `toml:"api_key"`
		TimeoutMS    *int    `toml:"timeout_ms"`
		DecideAPI    *string `toml:"decide_api"`
		DecideBuild  *string `toml:"decide_build"`
		Decide       *string `toml:"decide"`
		Verify       *string `toml:"verify"`
		Ground       *string `toml:"ground"`
		Ask          *string `toml:"ask"`
	} `toml:"model"`
	Safety *struct {
		Allow      []string `toml:"allow"`
		KillHotkey *string  `toml:"kill_hotkey"`
		Indicator  *bool    `toml:"indicator"`
		Confirm    *string  `toml:"confirm"`
	} `toml:"safety"`
	Tools *struct {
		Run     *bool   `toml:"run"`
		Plugins *string `toml:"plugins"`
	} `toml:"tools"`
	Driver *struct {
		Overlay *bool `toml:"overlay"`
	} `toml:"driver"`
	Desk *struct {
		Input       *string `toml:"input"`
		LockedInput *bool   `toml:"locked_input"`
	} `toml:"desk"`
	Daemon *struct {
		SocketGroup *string `toml:"socket_group"`
	} `toml:"daemon"`
}

// LoadConfig reads path. A missing file is the defaults. Errors name the
// file, key and line but never quote the line, so a secret under a wrong key
// is not printed.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	cfg.Path = path
	var f fileConfig
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return cfg, fmt.Errorf("%s: %s", path, describe(err, data))
	}
	bad := func(table, key, why string) error {
		return fmt.Errorf("%s:%d: [%s] %s %s", path, lineOf(data, table, key), table, key, why)
	}
	if m := f.Model; m != nil {
		set(&cfg.Model.BaseURL, m.BaseURL)
		set(&cfg.Model.DecisionsURL, m.DecisionsURL)
		set(&cfg.Model.APIKeyEnv, m.APIKeyEnv)
		if m.APIKey != nil {
			cfg.Model.APIKey = models.Secret(*m.APIKey)
		}
		set(&cfg.Model.TimeoutMS, m.TimeoutMS)
		if m.DecideAPI != nil {
			cfg.Model.DecideAPI = models.DecideAPI(*m.DecideAPI)
		}
		if m.DecideBuild != nil {
			v := *m.DecideBuild
			cfg.Model.DecideBuild = &v
		}
		set(&cfg.Model.Models.Decide, m.Decide)
		if m.Verify != nil {
			v := *m.Verify
			cfg.Model.Models.Verify = &v
		}
		set(&cfg.Model.Models.Ground, m.Ground)
		set(&cfg.Model.Models.Ask, m.Ask)
		if err := cfg.Model.Validate(); err != nil {
			return cfg, fmt.Errorf("%s: [model] %v", path, err)
		}
		if m.APIKey != nil {
			if err := cfg.Model.CheckKeyFile(path); err != nil {
				return cfg, err
			}
		}
	}
	if s := f.Safety; s != nil {
		if s.Allow != nil {
			cfg.Safety.Allow = s.Allow
		}
		set(&cfg.Safety.KillHotkey, s.KillHotkey)
		set(&cfg.Safety.Indicator, s.Indicator)
		if s.Confirm != nil {
			if !oneOf(*s.Confirm, "off", "coordinate", "every") {
				return cfg, bad("safety", "confirm", "must be off, coordinate or every")
			}
			cfg.Safety.Confirm = *s.Confirm
		}
	}
	if t := f.Tools; t != nil {
		set(&cfg.Tools.Run, t.Run)
		set(&cfg.Tools.Plugins, t.Plugins)
	}
	if d := f.Driver; d != nil {
		set(&cfg.Driver.Overlay, d.Overlay)
	}
	if d := f.Desk; d != nil {
		if d.Input != nil {
			if !oneOf(*d.Input, "foreground", "background", "auto") {
				return cfg, bad("desk", "input", "must be foreground, background or auto")
			}
			cfg.Desk.Input = *d.Input
		}
		set(&cfg.Desk.LockedInput, d.LockedInput)
	}
	if d := f.Daemon; d != nil && d.SocketGroup != nil {
		if strings.TrimSpace(*d.SocketGroup) == "" {
			return cfg, bad("daemon", "socket_group", "is empty; remove it to keep the socket private")
		}
		cfg.Daemon.SocketGroup = *d.SocketGroup
	}
	return cfg, nil
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// describe renders a decode error with key and line only.
func describe(err error, data []byte) string {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		var parts []string
		for _, e := range strict.Errors {
			row, _ := e.Position()
			parts = append(parts, fmt.Sprintf("line %d: unknown key %s", row, strings.Join(e.Key(), ".")))
		}
		return strings.Join(parts, "; ")
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, _ := de.Position()
		key := strings.Join(de.Key(), ".")
		if key == "" {
			key = keyAt(data, row)
		}
		if key != "" && strings.Contains(de.Error(), "cannot decode") {
			return fmt.Sprintf("line %d: %s has the wrong type", row, key)
		}
		if key != "" {
			return fmt.Sprintf("line %d: bad value for %s", row, key)
		}
		return fmt.Sprintf("line %d: not valid TOML", row)
	}
	// Type mismatches arrive as plain errors that name the Go type; keep
	// only the TOML side.
	msg := err.Error()
	if i := strings.Index(msg, "cannot decode"); i >= 0 {
		return "a value has the wrong type: " + regexp.MustCompile(`into a Go value of type [^ ]+`).ReplaceAllString(msg[i:], "")
	}
	return "cannot be read"
}

// lineOf finds the line of key within [table], 1-based; 0 when not found.
func lineOf(data []byte, table, key string) int {
	current := ""
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	for i, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			current = strings.Trim(t, "[] ")
			continue
		}
		if current == table && keyRe.MatchString(line) {
			return i + 1
		}
	}
	return 0
}

// keyAt is the key named on a line (the text before '='), never its value.
func keyAt(data []byte, row int) string {
	lines := strings.Split(string(data), "\n")
	if row < 1 || row > len(lines) {
		return ""
	}
	k, _, ok := strings.Cut(lines[row-1], "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
}
