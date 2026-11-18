package transcript

import (
	"encoding/json"
	"strings"
)

// Pi writes {"type":"message","message":{"role":…,"content":[…]}} lines.
// An assistant message's stopReason says whether the turn goes on
// ("toolUse") or ended ("stop", "aborted", "error", "length").
// {"type":"compaction"} marks a summarized history.

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Data      string          `json:"data"`
	MimeType  string          `json:"mimeType"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func pi(kind string, raw json.RawMessage) []Item {
	if kind == "compaction" {
		return []Item{{Kind: "compacted"}}
	}
	var message struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stopReason"`
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
			case b.Type == "text":
				texts = append(texts, b.Text)
			case b.Type == "image" && strings.HasPrefix(b.MimeType, "image/") && b.Data != "":
				images = append(images, "data:"+b.MimeType+";base64,"+b.Data)
			}
		}
		return userItem(texts, images)
	case "assistant":
		var items []Item
		for _, b := range blocks {
			switch b.Type {
			case "text":
				items = append(items, agentItem(b.Text)...)
			case "toolCall":
				items = append(items, describe(b.Name, b.Arguments)...)
			}
		}
		switch message.StopReason {
		case "stop", "length":
			items = append(items, end(0, ""))
		case "aborted":
			items = append(items, end(0, "interrupted"))
		case "error":
			items = append(items, end(0, "error"))
		}
		return items
	}
	return nil // system prompts and tool results
}
