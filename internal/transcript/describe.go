package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Tool calls read as steps, Codex style: a verb and what it acted on.

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > 120 {
		return string(runes[:119]) + "…"
	}
	return text
}

func step(verb, target string) Item { return Item{Kind: "tool", Tool: verb, Text: oneLine(target)} }

func base(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 && i < len(path)-1 {
		return path[i+1:]
	}
	return path
}

// firstLine keeps the first line of a command; continuation shows as "…".
func firstLine(command string) string {
	command = strings.TrimSpace(command)
	if i := strings.IndexByte(command, '\n'); i >= 0 {
		return strings.TrimSpace(command[:i]) + " …"
	}
	return command
}

var (
	jsCommand   = regexp.MustCompile(`cmd\s*:\s*"((?:[^"\\]|\\.)*)"`)
	patchTarget = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)
)

// describe turns one tool call into the steps a person reads, Codex style:
// a verb and what it acted on ("Ran go test ./...", "Read panel.css").
// Bookkeeping tools (plans, todo lists) produce no steps.
func describe(name string, raw json.RawMessage) []Item {
	var input map[string]any
	json.Unmarshal(raw, &input)
	text := func(key string) string { value, _ := input[key].(string); return value }
	switch strings.ToLower(name) {
	case "bash", "exec_command", "shell", "local_shell", "container.exec":
		command := text("command")
		if command == "" {
			command = text("cmd")
		}
		if parts, ok := input["command"].([]any); ok {
			words := make([]string, 0, len(parts))
			for _, part := range parts {
				if word, ok := part.(string); ok {
					words = append(words, word)
				}
			}
			command = strings.Join(words, " ")
			command = strings.TrimPrefix(strings.TrimPrefix(command, "bash -lc "), "bash -c ")
		}
		return []Item{step("ran", firstLine(command))}
	case "exec":
		// Codex code mode: JavaScript that calls tools.exec_command({cmd: "…"}).
		var items []Item
		// The input is the script itself (custom tool) or a JSON string or object holding it.
		source := string(raw)
		var script string
		if json.Unmarshal(raw, &script) == nil {
			source = script
		}
		for _, match := range jsCommand.FindAllStringSubmatch(source+"\n"+text("code"), -1) {
			var command string
			if json.Unmarshal([]byte(`"`+match[1]+`"`), &command) != nil {
				command = match[1]
			}
			items = append(items, step("ran", firstLine(command)))
		}
		if len(items) == 0 {
			return []Item{step("ran", "a script")}
		}
		return items
	case "write_stdin":
		if text("chars") == "" {
			return []Item{step("waited", "for a running command")}
		}
		return []Item{step("typed", text("chars"))}
	case "read", "view":
		return []Item{step("read", base(text("file_path")+text("path")))}
	case "view_image":
		return []Item{step("viewed", base(text("path")))}
	case "write":
		return []Item{step("created", base(text("file_path")+text("path")))}
	case "edit", "multiedit", "notebookedit":
		return []Item{step("edited", base(text("file_path")+text("path")+text("notebook_path")))}
	case "apply_patch":
		var items []Item
		for _, match := range patchTarget.FindAllStringSubmatch(string(raw)+text("input"), -1) {
			items = append(items, step("edited", base(strings.TrimSpace(match[1]))))
		}
		if len(items) == 0 {
			return []Item{step("edited", "files")}
		}
		return items
	case "grep", "glob", "search", "web_search", "websearch":
		target := text("pattern")
		if target == "" {
			target = text("query")
		}
		return []Item{step("searched", target)}
	case "webfetch":
		return []Item{step("fetched", text("url"))}
	case "task", "agent":
		return []Item{step("delegated", text("description"))}
	case "todowrite", "update_plan":
		return nil
	}
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:] // mcp__server__tool
	}
	return []Item{step("called", name)}
}
