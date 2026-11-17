// Package transcript turns a coding agent's own session log (Claude Code
// projects/*.jsonl, Codex sessions/**/rollout-*.jsonl, Pi
// ~/.pi/agent/sessions/--<cwd>--/*.jsonl, Kimi Code
// ~/.kimi-code/sessions/*/session_*/agents/main/wire.jsonl) into chat items for
// Fennec clients. It reads one JSON line at a time, so callers can feed new
// lines as the file grows. Herdr supplies live state; this supplies content.
package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// Version changes whenever Parse reads a log differently. Clients that cache
// parsed items drop a cache made by another version and read the log again.
const Version = 4

// Item is one entry in the conversation. Consecutive tool items are grouped
// by the client into one collapsed step.
type Item struct {
	ID   int64  `json:"id"`   // byte offset of the line, stable across reads
	Kind string `json:"kind"` // "user", "agent", "tool", or "compacted" (context was summarized)
	Text string `json:"text"` // message text, or what a tool acted on
	// For tools, the verb a person reads first: ran, read, edited, created,
	// searched, viewed, fetched, waited, typed, delegated or called.
	Tool string `json:"tool,omitempty"`
	Time int64  `json:"time,omitempty"` // Unix milliseconds
	// Images attached to a user message, as data: URLs.
	Images []string `json:"images,omitempty"`
}

// Parse returns the items in one log line. offset identifies the line.
func Parse(line []byte, offset int64) []Item {
	var record map[string]json.RawMessage
	if json.Unmarshal(line, &record) != nil {
		return nil
	}
	var kind string
	json.Unmarshal(record["type"], &kind)
	when := timestamp(record["timestamp"])
	var items []Item
	switch kind {
	case "user", "assistant":
		items = claude(kind, record)
	case "response_item":
		items = codex(record["payload"])
	case "compacted", "compaction":
		items = []Item{{Kind: "compacted"}} // Codex or Pi replaced its history with a summary
	case "message":
		items = pi(record["message"])
	case "attachment":
		items = queued(record["attachment"])
	case "context.append_message", "context.append_loop_event":
		items = kimi(kind, record)
		var ms int64
		if json.Unmarshal(record["time"], &ms) == nil && ms > 0 {
			when = ms
		}
	}
	for i := range items {
		items[i].ID = offset*16 + int64(i)
		items[i].Time = when
	}
	return items
}

func timestamp(raw json.RawMessage) int64 {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

// hidden reports text the agent injects into user turns (command wrappers,
// reminders, environment blocks, Codex's AGENTS.md and file notes), which
// the person never typed.
func hidden(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{"<", "Caveat:", "# AGENTS.md instructions for", "# Files mentioned by the user:", "Codex could not read the local image"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return trimmed == ""
}

// imagePlaceholder matches the markers agents leave in the text where an
// image was pasted: "[Image #2]" and Claude Code's "[Image: source: /tmp/x.png]".
var imagePlaceholder = regexp.MustCompile(`\[Image(?: #\d+|: source: [^\]\n]*)\]`)

var (
	// A message that starts with pasted text arrives wrapped in these tags,
	// sometimes followed by a note about where the pasted images are kept.
	pastedTag = regexp.MustCompile(`</?pasted_content[^>]*>`)
	imageNote = regexp.MustCompile(`(?s)\(The images above, .*?the image itself is in the message\.\)`)
	// Claude Code delivers a message sent mid-turn inside the next tool result.
	steering = regexp.MustCompile(`(?s)The user sent a new message while you were working:\n(.*?)\n\nThis is how Claude Code surfaces messages[^\n]*`)
	// What may follow the appended messages: notes about their images and
	// the closing tags of the notification that carried them.
	imageNotes = regexp.MustCompile(`(?m)^\[Image: source: [^\]\n]*\]$|^\[image result\] [^\n]*$|^</(?:result|task-notification)>$|\(The images above, [^\n]*\)`)
)

// unwrap keeps what the person pasted and drops the wrapper around it.
func unwrap(text string) string {
	return imageNote.ReplaceAllString(pastedTag.ReplaceAllString(text, ""), "")
}

func withoutPlaceholders(text string) string {
	lines := strings.Split(imagePlaceholder.ReplaceAllString(text, ""), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(strings.Join(strings.Fields(lines[i]), " "), " ")
	}
	return strings.TrimSpace(regexp.MustCompile(`\n{3,}`).ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// userItem joins a user turn's text and images into one message.
func userItem(texts, images []string) []Item {
	text := withoutPlaceholders(strings.Join(texts, "\n"))
	if text == "" && len(images) == 0 {
		return nil
	}
	return []Item{{Kind: "user", Text: text, Images: images}}
}

type block struct {
	Type   string          `json:"type"`
	Text   string          `json:"text"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
	Content json.RawMessage `json:"content"` // a tool_result's output
}

func imageURL(b block) string {
	if b.Type == "image" && b.Source.Type == "base64" && strings.HasPrefix(b.Source.MediaType, "image/") && b.Source.Data != "" {
		return "data:" + b.Source.MediaType + ";base64," + b.Source.Data
	}
	return ""
}

// kimi reads Kimi Code's wire log: the main agent's user messages
// (context.append_message, origin user; injected reminders are skipped) and
// its loop events (text parts and tool calls). Thinking is not shown.
func kimi(kind string, record map[string]json.RawMessage) []Item {
	var agent string
	json.Unmarshal(record["agentId"], &agent)
	if agent != "" && agent != "main" {
		return nil
	}
	if kind == "context.append_message" {
		var message struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
			Origin struct {
				Kind string `json:"kind"`
			} `json:"origin"`
		}
		if json.Unmarshal(record["message"], &message) != nil || message.Role != "user" || message.Origin.Kind != "user" {
			return nil
		}
		var texts, images []string
		for _, c := range message.Content {
			switch {
			case c.Type == "text" && !hidden(c.Text):
				texts = append(texts, c.Text)
			case strings.HasPrefix(c.ImageURL.URL, "data:image/"):
				images = append(images, c.ImageURL.URL)
			}
		}
		return userItem(texts, images)
	}
	var event struct {
		Type string `json:"type"`
		Part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"part"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	if json.Unmarshal(record["event"], &event) != nil {
		return nil
	}
	switch {
	case event.Type == "content.part" && event.Part.Type == "text" && strings.TrimSpace(event.Part.Text) != "":
		return []Item{{Kind: "agent", Text: strings.TrimSpace(event.Part.Text)}}
	case event.Type == "tool.call":
		return describe(event.Name, event.Args)
	}
	return nil
}

// queued reads a message the person sent while Claude Code was busy, which
// it records as {"type":"attachment","attachment":{"type":"queued_command",…}}.
// The same message may also appear in a tool result (with its images); the
// client merges the two.
func queued(raw json.RawMessage) []Item {
	var attachment struct {
		Type      string `json:"type"`
		Prompt    string `json:"prompt"`
		HumanTurn bool   `json:"humanTurn"`
		Origin    struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	}
	if json.Unmarshal(raw, &attachment) != nil || attachment.Type != "queued_command" {
		return nil
	}
	if !attachment.HumanTurn && attachment.Origin.Kind != "human" {
		// A background task's notification can carry messages sent meanwhile.
		return steeredText(attachment.Prompt)
	}
	if text := unwrap(attachment.Prompt); !hidden(text) {
		return userItem([]string{text}, nil)
	}
	return nil
}

// steeredText pulls mid-turn messages out of text Claude Code added beside or
// at the end of a tool result. Only the messages it appended count: they end
// the text, one after another. The same words earlier in a tool's output
// (a log the agent printed, say) are output, not messages.
func steeredText(text string) []Item {
	text = imageNotes.ReplaceAllString(text, "")
	matches := steering.FindAllStringSubmatchIndex(text, -1)
	first := len(matches)
	for end := len(text); first > 0; first-- {
		match := matches[first-1]
		if strings.TrimSpace(text[match[1]:end]) != "" {
			break
		}
		end = match[0]
	}
	var items []Item
	for _, match := range matches[first:] {
		items = append(items, userItem([]string{unwrap(text[match[2]:match[3]])}, nil)...)
	}
	return items
}

// steered returns the messages the person sent while a tool ran, which
// Claude Code appends to that tool's result, with the images they pasted.
func steered(result json.RawMessage) []Item {
	var text string
	var images []string
	if json.Unmarshal(result, &text) != nil {
		var parts []block
		json.Unmarshal(result, &parts)
		for _, part := range parts {
			if part.Type == "text" {
				text += part.Text + "\n"
			} else if url := imageURL(part); url != "" {
				images = append(images, url)
			}
		}
	}
	items := steeredText(text)
	// The images pasted with the messages come after the tool's output.
	if len(items) > 0 && len(images) > 0 {
		items[len(items)-1].Images = images
	}
	return items
}

func claude(kind string, record map[string]json.RawMessage) []Item {
	var flags struct {
		Sidechain bool `json:"isSidechain"`
		Meta      bool `json:"isMeta"`
	}
	raw, _ := json.Marshal(map[string]json.RawMessage{"isSidechain": record["isSidechain"], "isMeta": record["isMeta"]})
	json.Unmarshal(raw, &flags)
	if flags.Sidechain || flags.Meta {
		return nil
	}
	// Claude Code's summary after compaction: a marker, not something typed.
	var compact bool
	json.Unmarshal(record["isCompactSummary"], &compact)
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(record["message"], &message) != nil {
		return nil
	}
	var text string
	if kind == "user" && (compact || strings.HasPrefix(strings.TrimSpace(firstText(message.Content)), compactionPrefix)) {
		return []Item{{Kind: "compacted"}}
	}
	if json.Unmarshal(message.Content, &text) == nil {
		if text = unwrap(text); kind == "user" && !hidden(text) {
			return userItem([]string{text}, nil)
		}
		return nil
	}
	var blocks []block
	if json.Unmarshal(message.Content, &blocks) != nil {
		return nil
	}
	if kind == "user" {
		var texts, images []string
		var steers []Item
		for _, b := range blocks {
			switch {
			case b.Type == "text" && steering.MatchString(b.Text):
				steers = append(steers, steeredText(b.Text)...)
			case b.Type == "text" && !hidden(unwrap(b.Text)):
				texts = append(texts, unwrap(b.Text))
			case imageURL(b) != "":
				images = append(images, imageURL(b))
			case b.Type == "tool_result":
				steers = append(steers, steered(b.Content)...)
			}
		}
		if len(texts) == 0 && len(images) == 0 {
			return steers // tool results carry only messages sent mid-turn
		}
		return append(steers, userItem(texts, images)...)
	}
	var items []Item
	for _, b := range blocks {
		switch {
		case b.Type == "text" && kind == "assistant" && strings.TrimSpace(b.Text) != "":
			items = append(items, Item{Kind: "agent", Text: strings.TrimSpace(b.Text)})
		case b.Type == "tool_use" && kind == "assistant":
			items = append(items, describe(b.Name, b.Input)...)
		}
	}
	return items
}

const compactionPrefix = "This session is being continued from a previous conversation"

// firstText is a message's text, or its first text block's.
func firstText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []block
	json.Unmarshal(content, &blocks)
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Data      string          `json:"data"`
	MimeType  string          `json:"mimeType"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// pi reads Pi's {"type":"message","message":{"role":…,"content":[…]}}:
// text, image and toolCall blocks; system prompts and tool results are skipped.
func pi(raw json.RawMessage) []Item {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return nil
	}
	var blocks []piBlock
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		blocks = []piBlock{{Type: "text", Text: text}}
	} else if json.Unmarshal(message.Content, &blocks) != nil {
		return nil
	}
	switch message.Role {
	case "user":
		var texts, images []string
		for _, b := range blocks {
			switch {
			case b.Type == "text" && !hidden(b.Text):
				texts = append(texts, b.Text)
			case b.Type == "image" && strings.HasPrefix(b.MimeType, "image/") && b.Data != "":
				images = append(images, "data:"+b.MimeType+";base64,"+b.Data)
			}
		}
		if len(texts) > 0 && strings.HasPrefix(strings.TrimSpace(texts[0]), compactionPrefix) {
			return []Item{{Kind: "compacted"}}
		}
		if len(texts) == 0 && len(images) == 0 {
			return nil
		}
		return userItem(texts, images)
	case "assistant":
		var items []Item
		for _, b := range blocks {
			switch {
			case b.Type == "text" && strings.TrimSpace(b.Text) != "":
				items = append(items, Item{Kind: "agent", Text: strings.TrimSpace(b.Text)})
			case b.Type == "toolCall":
				items = append(items, describe(b.Name, b.Arguments)...)
			}
		}
		return items
	}
	return nil
}

func codex(payload json.RawMessage) []Item {
	var item struct {
		Type      string `json:"type"`
		Role      string `json:"role"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Input     string `json:"input"`
		Content   []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &item) != nil {
		return nil
	}
	switch item.Type {
	case "message":
		if item.Role == "user" {
			var texts, images []string
			for _, c := range item.Content {
				switch {
				case c.Type == "input_text" && !hidden(c.Text):
					texts = append(texts, c.Text)
				case c.Type == "input_image" && strings.HasPrefix(c.ImageURL, "data:image/"):
					images = append(images, c.ImageURL)
				}
			}
			if len(texts) == 0 {
				return nil // Codex's injected turns carry no typed text
			}
			return userItem(texts, images)
		}
		var items []Item
		for _, c := range item.Content {
			switch {
			case item.Role == "assistant" && c.Type == "output_text" && strings.TrimSpace(c.Text) != "":
				items = append(items, Item{Kind: "agent", Text: strings.TrimSpace(c.Text)})
			}
		}
		return items
	case "function_call", "custom_tool_call", "local_shell_call":
		input := item.Arguments
		if input == "" {
			input = item.Input
		}
		return describe(item.Name, json.RawMessage(input))
	}
	return nil
}

// toolSummary picks the most telling argument (a command, a path, a
// description) and keeps one short line of it.
func toolSummary(raw json.RawMessage) string {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return oneLine(string(raw))
	}
	for _, key := range []string{"description", "command", "cmd", "file_path", "path", "pattern", "query", "url", "prompt"} {
		switch value := input[key].(type) {
		case string:
			return oneLine(value)
		case []any:
			parts := make([]string, 0, len(value))
			for _, part := range value {
				if text, ok := part.(string); ok {
					parts = append(parts, text)
				}
			}
			return oneLine(strings.Join(parts, " "))
		}
	}
	return ""
}

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
