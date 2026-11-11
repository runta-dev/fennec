package hookbridge

import (
	"encoding/json"
	"fennec/internal/sessionid"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"sort"
	"strings"
)

type Resolver func(event, command string) (*Binding, error)

// RewriteCodex edits exact handler values only; unknown hooks keep their
// original behavior. A resolver may return nil to leave a sandbox hook alone.
func RewriteCodex(arguments []string, session string, resolve Resolver) ([]string, []Binding, error) {
	if !sessionid.Valid(session) {
		return nil, nil, fmt.Errorf("invalid hook session")
	}
	result := append([]string(nil), arguments...)
	var bindings []Binding
	seen := map[string]bool{}
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
		rewritten, found, err := rewriteCodexValue(value, session, resolve)
		if err != nil {
			return nil, nil, err
		}
		result[index] = prefix + rewritten
		for _, binding := range found {
			if !seen[binding.ID] {
				bindings = append(bindings, binding)
				seen[binding.ID] = true
			}
		}
	}
	if err := ValidateBindings(bindings); err != nil {
		return nil, nil, err
	}
	return result, bindings, nil
}
func rewriteCodexValue(value, session string, resolve Resolver) (string, []Binding, error) {
	var document map[string]any
	if toml.Unmarshal([]byte(value), &document) != nil {
		return value, nil, nil
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	var bindings []Binding
	parser := unstable.Parser{}
	parser.Reset([]byte(value))
	for parser.NextExpression() {
		expression := parser.Expression()
		if expression.Kind != unstable.KeyValue {
			continue
		}
		names := keys(expression)
		if len(names) != 2 || names[0] != "hooks" || expression.Value().Kind != unstable.Array {
			continue
		}
		groups := expression.Value().Children()
		for groups.Next() {
			group := groups.Node()
			if group.Kind != unstable.InlineTable {
				continue
			}
			fields := group.Children()
			for fields.Next() {
				field := fields.Node()
				key := keys(field)
				if len(key) != 1 || key[0] != "hooks" || field.Value().Kind != unstable.Array {
					continue
				}
				handlers := field.Value().Children()
				for handlers.Next() {
					handler := handlers.Node()
					if handler.Kind != unstable.InlineTable {
						continue
					}
					var command, timeout *unstable.Node
					kind := ""
					items := handler.Children()
					for items.Next() {
						item := items.Node()
						k := keys(item)
						if len(k) != 1 {
							continue
						}
						switch k[0] {
						case "type":
							kind = string(item.Value().Data)
						case "command":
							command = item.Value()
						case "timeout":
							timeout = item.Value()
						}
					}
					if kind != "command" || command == nil || command.Kind != unstable.String {
						continue
					}
					binding, err := resolve(names[1], string(command.Data))
					if err != nil {
						return value, nil, err
					}
					if binding == nil {
						continue
					}
					if err = binding.Validate(); err != nil {
						return value, nil, err
					}
					raw, _ := json.Marshal(`"$HOME/.local/fennec/bin/fennec-terminal" hook ` + session + " " + binding.ID)
					replacement := string(raw)
					budget := fmt.Sprint(binding.Wait().Milliseconds() + 1000)
					if timeout == nil {
						replacement += ",timeout=" + budget
					} else {
						edits = append(edits, edit{int(timeout.Raw.Offset), int(timeout.Raw.Offset + timeout.Raw.Length), budget})
					}
					edits = append(edits, edit{int(command.Raw.Offset), int(command.Raw.Offset + command.Raw.Length), replacement})
					bindings = append(bindings, *binding)
				}
			}
		}
	}
	if parser.Error() != nil {
		return value, nil, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := value
	for _, e := range edits {
		if e.start < 0 || e.end > len(out) || e.start > e.end {
			return value, nil, fmt.Errorf("invalid hook source range")
		}
		out = out[:e.start] + e.text + out[e.end:]
	}
	return out, bindings, nil
}
func keys(node *unstable.Node) []string {
	if node.Kind != unstable.KeyValue {
		return nil
	}
	var result []string
	i := node.Key()
	for i.Next() {
		result = append(result, string(i.Node().Data))
	}
	return result
}
