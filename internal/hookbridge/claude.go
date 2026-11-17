package hookbridge

import (
	"bytes"
	"encoding/json"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ClaudeSettings carries a private settings snapshot over the resident channel,
// not in argv. Index identifies the placeholder argument to replace on the guest.
type ClaudeSettings struct {
	Index int             `json:"index"`
	Data  json.RawMessage `json:"data"`
}

const SettingsPlaceholder = "fennec:claude-settings"
const SettingsLimit = 256 << 10

// Native --continue import can insert a resume ID before the settings flags.
func ReindexClaudeSettings(args []string, files []ClaudeSettings) ([]ClaudeSettings, error) {
	if len(files) == 0 {
		return files, nil
	}
	result := append([]ClaudeSettings(nil), files...)
	next := 0
	for i, arg := range args {
		if arg == SettingsPlaceholder && i > 0 && args[i-1] == "--settings" {
			if next >= len(result) {
				return nil, fmt.Errorf("unexpected settings placeholder")
			}
			result[next].Index = i
			next++
		}
	}
	if next != len(result) {
		return nil, fmt.Errorf("missing settings placeholder")
	}
	return result, nil
}

func ValidateClaudeSettings(agent string, args []string, files []ClaudeSettings) error {
	total := 0
	seen := map[int]bool{}
	for _, f := range files {
		total += len(f.Data)
		if agent != "claude" || f.Index <= 0 || f.Index >= len(args) || args[f.Index-1] != "--settings" || args[f.Index] != SettingsPlaceholder || seen[f.Index] || total > SettingsLimit || !json.Valid(f.Data) || !bytes.HasPrefix(bytes.TrimSpace(f.Data), []byte("{")) {
			return fmt.Errorf("invalid Claude settings snapshot")
		}
		seen[f.Index] = true
	}
	if len(files) > 16 {
		return fmt.Errorf("too many Claude settings files")
	}
	return nil
}

// PrepareClaude snapshots file and inline settings before the Mac wrapper can
// remove its temporary file. Only recognized handlers are changed.
func PrepareClaude(arguments []string, session string, resolve Resolver) ([]string, []Binding, []ClaudeSettings, error) {
	if !sessionid.Valid(session) {
		return nil, nil, nil, fmt.Errorf("invalid hook session")
	}
	result := append([]string(nil), arguments...)
	var files []ClaudeSettings
	var bindings []Binding
	seen := map[string]bool{}
	for i := 0; i < len(result); i++ {
		if result[i] == "--" {
			break
		}
		// Values of other flags (including prompts) must never become options.
		switch result[i] {
		case "--system-prompt", "--append-system-prompt", "--system-prompt-file", "--append-system-prompt-file", "--model", "--fallback-model", "--permission-mode", "--output-format", "--input-format", "--session-id", "--agent", "--agents", "--mcp-config", "--setting-sources", "--name":
			i++
			continue
		case "-p", "--print", "--resume", "-r":
			if i+1 < len(result) && !strings.HasPrefix(result[i+1], "--") {
				i++
			}
			continue
		}
		value := ""
		if result[i] == "--settings" {
			if i+1 >= len(result) {
				break
			}
			i++
			value = result[i]
		} else if strings.HasPrefix(result[i], "--settings=") {
			value = strings.TrimPrefix(result[i], "--settings=")
			result = append(result[:i], append([]string{"--settings", value}, result[i+1:]...)...)
			i++
		} else {
			continue
		}
		raw := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			file, err := os.Open(value)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("cannot read Claude settings on this computer")
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > SettingsLimit {
				file.Close()
				return nil, nil, nil, fmt.Errorf("invalid Claude settings file")
			}
			raw, err = io.ReadAll(io.LimitReader(file, SettingsLimit+1))
			file.Close()
			if err != nil {
				return nil, nil, nil, fmt.Errorf("cannot read Claude settings on this computer")
			}
		}
		if len(raw) > SettingsLimit {
			return nil, nil, nil, fmt.Errorf("Claude settings exceed limit")
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil || doc == nil {
			return nil, nil, nil, fmt.Errorf("invalid Claude settings JSON")
		}
		if h, ok := doc["hooks"]; ok && resolve != nil {
			var events map[string][]map[string]json.RawMessage
			if json.Unmarshal(h, &events) != nil {
				return nil, nil, nil, fmt.Errorf("invalid Claude hooks")
			}
			for event, groups := range events {
				for _, group := range groups {
					var handlers []map[string]json.RawMessage
					if json.Unmarshal(group["hooks"], &handlers) != nil {
						continue
					}
					for _, handler := range handlers {
						var kind, command string
						json.Unmarshal(handler["type"], &kind)
						json.Unmarshal(handler["command"], &command)
						if kind != "command" {
							continue
						}
						binding, err := resolve(event, command)
						if err != nil {
							return nil, nil, nil, err
						}
						if binding == nil {
							continue
						}
						if err := binding.Validate(); err != nil {
							return nil, nil, nil, err
						}
						handler["command"], _ = json.Marshal(`"$HOME/.local/fennec/bin/fennec-terminal" hook ` + session + " " + binding.ID)
						handler["timeout"], _ = json.Marshal(int(binding.Wait().Seconds()) + 1)
						if !seen[binding.ID] {
							bindings = append(bindings, *binding)
							seen[binding.ID] = true
						}
					}
					group["hooks"], _ = json.Marshal(handlers)
				}
			}
			doc["hooks"], _ = json.Marshal(events)
		}
		raw, _ = json.Marshal(doc)
		files = append(files, ClaudeSettings{Index: i, Data: raw})
		result[i] = SettingsPlaceholder
	}
	if err := ValidateClaudeSettings("claude", result, files); err != nil {
		return nil, nil, nil, err
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].ID < bindings[j].ID })
	return result, bindings, files, ValidateBindings(bindings)
}

// MaterializeClaudeSettings keeps snapshots across detach/resume, without
// exposing their contents in the process list or overwriting a prior launch.
func MaterializeClaudeSettings(home, session, agent string, args []string, files []ClaudeSettings) ([]string, error) {
	if err := ValidateClaudeSettings(agent, args, files); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return args, nil
	}
	if !sessionid.Valid(session) {
		return nil, fmt.Errorf("invalid settings session")
	}
	dir := filepath.Join(home, ".local/fennec/launch-settings", session)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private settings directory required")
	}
	result := append([]string(nil), args...)
	for _, f := range files {
		path := filepath.Join(dir, fmt.Sprintf("%d.json", f.Index))
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			info, e := os.Lstat(path)
			if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return nil, fmt.Errorf("invalid existing settings file")
			}
			old, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(old, f.Data) {
				return nil, fmt.Errorf("session settings changed")
			}
		} else if err != nil {
			return nil, err
		} else {
			_, err = out.Write(f.Data)
			closeErr := out.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		result[f.Index] = path
	}
	return result, nil
}
