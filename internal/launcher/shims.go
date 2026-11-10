package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallShims creates only Fennec-owned wrappers. It never rewrites the original
// CLI or shell configuration. Unknown files in the destination are preserved.
func InstallShims(directory, executable string) error {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\x00\r\n") {
		return fmt.Errorf("absolute shim directory and executable required")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fmt.Errorf("executable unavailable")
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	marker := "# Fennec managed launcher v1\n"
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	commands := make(map[string]string, len(Supported)+1)
	for agent := range Supported {
		commands[agent] = " dispatch-agent " + quote(agent)
	}
	commands["fen"] = " fen"
	for agent, dispatch := range commands {
		path := filepath.Join(directory, agent)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("refusing to replace unmanaged shim")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(string(data), "#!/bin/sh\n"+marker) {
				return fmt.Errorf("refusing to replace unmanaged shim")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		script := "#!/bin/sh\n" + marker + "exec " + quote(executable) + dispatch + " \"$@\"\n"
		file, err := os.CreateTemp(directory, ".shim-")
		if err != nil {
			return err
		}
		_, err = file.WriteString(script)
		if err == nil {
			err = file.Chmod(0700)
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(file.Name(), path)
		}
		os.Remove(file.Name())
		if err != nil {
			return err
		}
	}
	return nil
}

// EnabledAgents is configuration, not a readiness claim. An enabled agent with
// an unavailable broker must fail rather than silently execute on this machine.
func EnabledAgents(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var value struct {
		Agents []string `json:"agents"`
	}
	if len(data) > 8192 || json.Unmarshal(data, &value) != nil {
		return nil, fmt.Errorf("invalid takeover configuration")
	}
	result := map[string]bool{}
	for _, id := range value.Agents {
		if !Supported[id] {
			return nil, fmt.Errorf("unsupported takeover agent")
		}
		result[id] = true
	}
	return result, nil
}

// PublishEnabled writes only routing preferences, never login credentials.
func PublishEnabled(path string, agents []string) error {
	for _, id := range agents {
		if !Supported[id] {
			return fmt.Errorf("unsupported takeover agent")
		}
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute takeover configuration required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Agents []string `json:"agents"`
	}{agents})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".takeover-")
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
