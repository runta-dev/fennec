package hookbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

type Config struct {
	Version int          `json:"version"`
	Hooks   []Definition `json:"hooks"`
}

func ConfigPath(home string) string { return filepath.Join(home, ".config/fennec/hooks.json") }
func ParseConfig(raw []byte) (Config, error) {
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 256<<10 || decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
		return cfg, fmt.Errorf("invalid hook configuration")
	}
	if cfg.Version != 1 || len(cfg.Hooks) > 64 {
		return cfg, fmt.Errorf("unsupported hook configuration")
	}
	seen := map[string]bool{}
	selectors := map[string]bool{}
	for i, d := range cfg.Hooks {
		d = d.Normalized()
		selector := d.Agent + "\x00" + d.Event + "\x00" + d.Command
		if d.Validate() != nil || seen[d.Name] || selectors[selector] {
			return cfg, fmt.Errorf("invalid or duplicate hook definition")
		}
		seen[d.Name] = true
		selectors[selector] = true
		cfg.Hooks[i] = d
	}
	return cfg, nil
}
func LoadConfig(home string) (Config, error) {
	data, err := os.ReadFile(ConfigPath(home))
	if os.IsNotExist(err) {
		return Config{Version: 1}, nil
	}
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(data)
}
func SaveConfig(home string, cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if _, err = ParseConfig(data); err != nil {
		return err
	}
	path := ConfigPath(home)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func ConfigExecutors(home string) (map[string]*CommandExecutor, error) {
	cfg, err := LoadConfig(home)
	if err != nil {
		return nil, err
	}
	result := map[string]*CommandExecutor{}
	for _, d := range cfg.Hooks {
		if d.Location != "local" {
			continue
		}
		executor, err := NewCommand(d, func(expected Definition) error {
			current, err := LoadConfig(home)
			if err != nil {
				return err
			}
			for _, value := range current.Hooks {
				if value.Name == expected.Name && reflect.DeepEqual(value, expected) {
					return nil
				}
			}
			return fmt.Errorf("hook configuration changed")
		})
		if err != nil {
			// An unavailable unrelated integration must not block every session.
			// Matching commands are rejected explicitly by the launch rewriter.
			continue
		}
		result[d.Name] = executor
	}
	return result, nil
}
