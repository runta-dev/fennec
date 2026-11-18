package transcript

import (
	"encoding/json"
	"strings"
)

// Kimi Code writes a wire log per agent. Only the main agent's records count:
//
//	context.append_message     a message; origin "user" is the person's,
//	                           "injection" is a reminder it added
//	context.append_loop_event  content.part (text, think) and tool.call
//	turn.ended                 the turn ended, with its reason and duration
func kimi(kind string, record map[string]json.RawMessage) []Item {
	var agent string
	json.Unmarshal(record["agentId"], &agent)
	if agent != "" && agent != "main" {
		return nil
	}
	switch kind {
	case "turn.ended":
		var ended struct {
			Reason     string `json:"reason"`
			DurationMS int64  `json:"durationMs"`
		}
		json.Unmarshal(record["reason"], &ended.Reason)
		json.Unmarshal(record["durationMs"], &ended.DurationMS)
		reason := ""
		switch ended.Reason {
		case "", "completed":
		case "cancelled", "canceled", "interrupted", "aborted":
			reason = "interrupted"
		default:
			reason = "error"
		}
		return []Item{end(ended.DurationMS, reason)}
	case "context.append_message":
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
			case c.Type == "text":
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
	case event.Type == "content.part" && event.Part.Type == "text":
		return agentItem(event.Part.Text)
	case event.Type == "tool.call":
		return describe(event.Name, event.Args)
	}
	return nil
}
