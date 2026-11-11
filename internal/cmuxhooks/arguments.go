// Package cmuxhooks adapts cmux's per-invocation Codex hooks across a cloud TUI.
package cmuxhooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fennec/internal/sessionid"
	shlex "github.com/anmitsu/go-shlex"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

var eventCommands = map[string][]string{
	"SessionStart": {"session-start"}, "UserPromptSubmit": {"prompt-submit", "user-prompt-submit", "inbox-drain"},
	"Stop": {"stop", "inbox-stop"}, "PreToolUse": {"pre-tool-use"}, "PostToolUse": {"post-tool-use"},
	"PermissionRequest": {"notification"}, "Notification": {"notification"}, "SubagentStart": {"subagent-start"},
	"SubagentStop": {"subagent-stop"}, "SessionEnd": {"session-end", "session-finalize"},
}

func ValidEvent(event string) bool {
	for _, values := range eventCommands {
		for _, value := range values {
			if value == event {
				return true
			}
		}
	}
	return false
}

var generatedName = regexp.MustCompile(`^cmux-codex-hook-(?:([0-9a-f]{16})-)?([a-z-]+)\.sh$`)

// Rewrite changes only generated cmux command values in actual -c/--config
// arguments. All unrelated TOML bytes, user hooks, and prompt arguments survive.
func Rewrite(arguments []string, home, id string) ([]string, int, error) {
	if !sessionid.Valid(id) {
		return nil, 0, fmt.Errorf("invalid cmux hook session")
	}
	result := append([]string(nil), arguments...)
	count := 0
	for i := 0; i < len(result); i++ {
		if result[i] == "--" {
			break
		}
		switch result[i] {
		case "--enable", "--disable", "--remote", "--remote-auth-token-env", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval":
			i++
			continue
		}
		index, prefix := -1, ""
		switch {
		case result[i] == "-c" || result[i] == "--config":
			if i+1 < len(result) {
				i++
				index = i
			}
		case strings.HasPrefix(result[i], "--config="):
			index = i
			prefix = "--config="
		case strings.HasPrefix(result[i], "-c") && len(result[i]) > 2:
			index = i
			prefix = "-c"
		}
		if index < 0 {
			continue
		}
		value := strings.TrimPrefix(result[index], prefix)
		if !strings.HasPrefix(strings.TrimSpace(value), "hooks.") {
			continue
		}
		rewritten, n, err := rewriteValue(value, home, id)
		if err != nil {
			return nil, 0, err
		}
		if n > 0 {
			result[index] = prefix + rewritten
			count += n
		}
	}
	return result, count, nil
}
func rewriteValue(value, home, id string) (string, int, error) {
	var document map[string]any
	if toml.Unmarshal([]byte(value), &document) != nil {
		return value, 0, nil
	}
	parser := unstable.Parser{}
	parser.Reset([]byte(value))
	type edit struct {
		start, end  int
		replacement string
	}
	var edits []edit
	for parser.NextExpression() {
		expression := parser.Expression()
		if expression.Kind != unstable.KeyValue {
			continue
		}
		keys := expression.Key()
		var names []string
		for keys.Next() {
			names = append(names, string(keys.Node().Data))
		}
		if len(names) != 2 || names[0] != "hooks" {
			continue
		}
		allowed := eventCommands[names[1]]
		if len(allowed) == 0 {
			continue
		}
		groups := expression.Value()
		if groups.Kind != unstable.Array {
			continue
		}
		groupIterator := groups.Children()
		for groupIterator.Next() {
			group := groupIterator.Node()
			if group.Kind != unstable.InlineTable {
				continue
			}
			fields := group.Children()
			for fields.Next() {
				field := fields.Node()
				if nodeKey(field) != "hooks" || field.Value().Kind != unstable.Array {
					continue
				}
				handlers := field.Value().Children()
				for handlers.Next() {
					handler := handlers.Node()
					if handler.Kind != unstable.InlineTable {
						continue
					}
					var command *unstable.Node
					kind := ""
					values := handler.Children()
					for values.Next() {
						item := values.Node()
						switch nodeKey(item) {
						case "type":
							kind = string(item.Value().Data)
						case "command":
							command = item.Value()
						}
					}
					if kind != "command" || command == nil || command.Kind != unstable.String {
						continue
					}
					event, err := recognize(string(command.Data), home, allowed)
					if err != nil {
						return value, 0, err
					}
					if event == "" {
						continue
					}
					replacement, _ := json.Marshal(`"$HOME/.local/fennec/bin/fennec-terminal" cmux-hook ` + id + " " + event)
					edits = append(edits, edit{int(command.Raw.Offset), int(command.Raw.Offset + command.Raw.Length), string(replacement)})
				}
			}
		}
	}
	if parser.Error() != nil {
		return value, 0, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := value
	for _, e := range edits {
		result = result[:e.start] + e.replacement + result[e.end:]
	}
	return result, len(edits), nil
}
func nodeKey(node *unstable.Node) string {
	if node.Kind != unstable.KeyValue {
		return ""
	}
	keys := node.Key()
	if !keys.Next() {
		return ""
	}
	key := string(keys.Node().Data)
	if keys.Next() {
		return ""
	}
	return key
}
func recognize(command, home string, allowed []string) (string, error) {
	parts, err := shlex.Split(command, true)
	if err != nil || len(parts) != 1 {
		return "", nil
	}
	path := parts[0]
	if !filepath.IsAbs(path) || filepath.Clean(filepath.Dir(path)) != filepath.Join(home, ".cmux/hooks") {
		return "", nil
	}
	match := generatedName.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return "", nil
	}
	event := match[2]
	found := false
	for _, candidate := range allowed {
		if candidate == event {
			found = true
		}
	}
	if !found {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return "", fmt.Errorf("cmux hook is unavailable on this Mac; restart the cmux terminal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read cmux hook")
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh\n") || !strings.Contains(string(data), "CMUX_SURFACE_ID") {
		return "", nil
	}
	if match[1] != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:8]) != match[1] {
			return "", fmt.Errorf("cmux hook changed after generation; restart the cmux terminal")
		}
	}
	return event, nil
}
