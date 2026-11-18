package transcript

import (
	"encoding/json"
	"strings"
)

// Codex writes {"type":…,"payload":…} lines:
//
//	response_item  messages (user, assistant, developer) and tool calls
//	event_msg      task_started, task_complete, turn_aborted, token counts…
//	compacted      its history was replaced by a summary
func codex(kind string, payload json.RawMessage) []Item {
	switch kind {
	case "compacted":
		return []Item{{Kind: "compacted"}}
	case "event_msg":
		var event struct {
			Type       string `json:"type"`
			Reason     string `json:"reason"`
			DurationMS int64  `json:"duration_ms"`
		}
		json.Unmarshal(payload, &event)
		switch event.Type {
		case "task_complete":
			return []Item{end(event.DurationMS, "")}
		case "turn_aborted":
			reason := event.Reason
			if reason == "" {
				reason = "interrupted"
			}
			return []Item{end(event.DurationMS, reason)}
		}
		return nil
	}
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
		switch item.Role {
		case "user":
			var texts, images []string
			for _, c := range item.Content {
				switch {
				case c.Type == "input_text":
					texts = append(texts, c.Text)
				case c.Type == "input_image" && strings.HasPrefix(c.ImageURL, "data:image/"):
					images = append(images, c.ImageURL)
				}
			}
			items := userItem(texts, images)
			if len(items) == 1 && items[0].Text == "" {
				return nil // Codex's injected turns carry images without typed text
			}
			return items
		case "assistant":
			var items []Item
			for _, c := range item.Content {
				if c.Type == "output_text" {
					items = append(items, agentItem(c.Text)...)
				}
			}
			return items
		}
		return nil // developer and system instructions
	case "function_call", "custom_tool_call", "local_shell_call":
		input := item.Arguments
		if input == "" {
			input = item.Input
		}
		return describe(item.Name, json.RawMessage(input))
	}
	return nil
}
