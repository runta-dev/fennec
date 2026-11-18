package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Claude Code writes one record per line:
//
//	user       the person's message, or tool results (content all tool_result)
//	assistant  text, thinking and tool_use blocks, one record per block
//	system     turn_duration when it stops working; compact_boundary
//	attachment queued_command: a message sent while it was working
//
// Sidechain records belong to subagents and meta records to its own
// bookkeeping; neither is part of the conversation.

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
}

func (b block) imageURL() string {
	if b.Type == "image" && b.Source.Type == "base64" && strings.HasPrefix(b.Source.MediaType, "image/") && b.Source.Data != "" {
		return "data:" + b.Source.MediaType + ";base64," + b.Source.Data
	}
	return ""
}

// midTurn is how Claude Code words a message sent while it works, when it
// adds the message beside a tool result.
var midTurn = regexp.MustCompile(`(?s)^\s*The user sent a new message while you were working:\n(.*?)\n\nThis is how Claude Code surfaces messages[^\n]*\s*$`)

func claude(kind string, record map[string]json.RawMessage) []Item {
	var flags struct {
		Sidechain     bool   `json:"isSidechain"`
		Meta          bool   `json:"isMeta"`
		CompactResume bool   `json:"isCompactSummary"`
		APIError      bool   `json:"isApiErrorMessage"`
		Subtype       string `json:"subtype"`
		DurationMS    int64  `json:"durationMs"`
	}
	for key, target := range map[string]any{"isSidechain": &flags.Sidechain, "isMeta": &flags.Meta, "isCompactSummary": &flags.CompactResume,
		"isApiErrorMessage": &flags.APIError, "subtype": &flags.Subtype, "durationMs": &flags.DurationMS} {
		json.Unmarshal(record[key], target)
	}
	if flags.Sidechain || flags.Meta {
		return nil
	}
	switch kind {
	case "system":
		switch flags.Subtype {
		case "turn_duration":
			return []Item{end(flags.DurationMS, "")}
		case "compact_boundary":
			return []Item{{Kind: "compacted"}}
		}
		return nil
	case "attachment":
		return queued(record["attachment"])
	}
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(record["message"], &message) != nil {
		return nil
	}
	var blocks []block
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		blocks = []block{{Type: "text", Text: text}}
	} else if json.Unmarshal(message.Content, &blocks) != nil {
		return nil
	}
	if kind == "assistant" {
		if flags.APIError {
			return []Item{end(0, "error")}
		}
		var items []Item
		for _, b := range blocks {
			switch b.Type {
			case "text":
				items = append(items, agentItem(b.Text)...)
			case "tool_use":
				items = append(items, describe(b.Name, b.Input)...)
			}
		}
		return items
	}
	// The summary that resumes a compacted conversation; compact_boundary
	// already marks the place.
	if flags.CompactResume {
		return nil
	}
	var texts, images []string
	var items []Item
	for _, b := range blocks {
		switch {
		case b.Type == "text" && interrupted.MatchString(strings.TrimSpace(b.Text)):
			items = append(items, end(0, "interrupted"))
		case b.Type == "text" && midTurn.MatchString(b.Text):
			items = append(items, steer(userItem([]string{midTurn.FindStringSubmatch(b.Text)[1]}, nil))...)
		case b.Type == "text":
			texts = append(texts, b.Text)
		case b.imageURL() != "":
			images = append(images, b.imageURL())
		}
		// tool_result blocks answer tool calls; they are not messages.
	}
	return append(items, userItem(texts, images)...)
}

// queued reads a message the person sent while Claude Code was working:
// {"type":"attachment","attachment":{"type":"queued_command",…}}. Only the
// person's own; background-task notifications queue the same way.
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
		return nil
	}
	return steer(userItem([]string{attachment.Prompt}, nil))
}

func steer(items []Item) []Item {
	for i := range items {
		items[i].Steer = true
	}
	return items
}
