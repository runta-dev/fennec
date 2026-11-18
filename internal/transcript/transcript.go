// Package transcript turns a coding agent's own session log (Claude Code
// projects/*.jsonl, Codex sessions/**/rollout-*.jsonl, Pi
// ~/.pi/agent/sessions/--<cwd>--/*.jsonl, Kimi Code
// ~/.kimi-code/sessions/*/session_*/agents/main/wire.jsonl) into chat items
// for Fennec clients.
//
// Parse reads one line at a time, so callers can feed new lines as a log
// grows, and it reports only what the agent itself recorded:
//
//   - user: a message the person sent. Records that hold only tool results
//     are not messages and never start a turn; nor are injected reminders,
//     command wrappers or interruption notices.
//   - agent, tool: the agent's text and its tool calls, in order.
//   - end: the agent's own record that it stopped (Claude Code turn_duration,
//     Codex task_complete or turn_aborted, Pi stopReason, Kimi turn.ended),
//     with its duration and whether the person interrupted it.
//   - compacted: the agent summarized its context.
//
// Turns are drawn by the client from these. Claude Code records a message
// sent while it works differently from one that starts a turn, so those
// carry steer; for the other agents, a message steers when it arrives before
// the agent recorded that it stopped.
// Herdr supplies live state; this supplies content.
package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// Version changes whenever Parse reads a log differently. Clients that cache
// parsed items drop a cache made by another version and read the log again.
const Version = 6

// Item is one entry in the conversation. Consecutive tool items are grouped
// by the client into one collapsed step.
type Item struct {
	ID   int64  `json:"id"`   // byte offset of the line, stable across reads
	Kind string `json:"kind"` // "user", "agent", "tool", "end" or "compacted"
	Text string `json:"text"` // message text, or what a tool acted on
	// For tools, the verb a person reads first: ran, read, edited, created,
	// searched, viewed, fetched, waited, typed, delegated or called.
	Tool string `json:"tool,omitempty"`
	Time int64  `json:"time,omitempty"` // Unix milliseconds
	// Images attached to a user message, as data: URLs.
	Images []string `json:"images,omitempty"`
	// For user: sent while the agent worked, to steer the running turn.
	Steer bool `json:"steer,omitempty"`
	// For end: how long the agent worked, in milliseconds, when it says, and
	// why it stopped: "interrupted", "error", or empty when it finished.
	Duration int64  `json:"duration,omitempty"`
	Reason   string `json:"reason,omitempty"`
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
	case "user", "assistant", "system", "attachment":
		items = claude(kind, record)
	case "response_item", "event_msg", "compacted":
		items = codex(kind, record["payload"])
	case "message", "compaction":
		items = pi(kind, record["message"])
	case "context.append_message", "context.append_loop_event", "turn.ended":
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

func end(duration int64, reason string) Item {
	return Item{Kind: "end", Duration: duration, Reason: reason}
}

// injected matches text an agent or its host adds to the person's turn:
// reminders, command wrappers and output, environment and instruction
// blocks, background-task notices. Each is named, so a message that merely
// starts with "<" (pasted HTML, say) still shows.
var injected = regexp.MustCompile(`^(?:<|&lt;)/?(image|system-reminder|command-name|command-message|command-args|local-command-stdout|local-command-stderr|local-command-caveat|bash-input|bash-stdout|bash-stderr|environment_context|user_instructions|user_shell_command|INSTRUCTIONS|turn_aborted|task-notification|ide_opened_file|ide_selection|user-prompt-submit-hook)\b`)

// hidden reports text the person never typed.
func hidden(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || injected.MatchString(trimmed) {
		return true
	}
	for _, prefix := range []string{"Caveat:", "# AGENTS.md instructions for", "# Files mentioned by the user:", "Codex could not read the local image", "Tool loaded.", "[SUGGESTION MODE:"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// interrupted matches the notice an agent records when the person stops it.
var interrupted = regexp.MustCompile(`^\[Request interrupted by user\b`)

var (
	// imagePlaceholder matches the markers left where an image was pasted:
	// "[Image #2]" and Claude Code's "[Image: source: /tmp/x.png]".
	imagePlaceholder = regexp.MustCompile(`\[Image(?: #\d+|: source: [^\]\n]*)\]`)
	pastedTag        = regexp.MustCompile(`</?pasted_content[^>]*>`)
	imageNote        = regexp.MustCompile(`(?s)\(The images above, .*?the image itself is in the message\.\)`)
	blankLines       = regexp.MustCompile(`\n{3,}`)
)

// unwrap drops the tags some terminals put around pasted text, keeping it.
func unwrap(text string) string {
	return imageNote.ReplaceAllString(pastedTag.ReplaceAllString(text, ""), "")
}

func withoutPlaceholders(text string) string {
	lines := strings.Split(imagePlaceholder.ReplaceAllString(text, ""), "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// userItem joins a message's text and images into one item.
func userItem(texts, images []string) []Item {
	var kept []string
	for _, text := range texts {
		if text = unwrap(text); !hidden(text) {
			kept = append(kept, text)
		}
	}
	text := withoutPlaceholders(strings.Join(kept, "\n"))
	if text == "" && len(images) == 0 {
		return nil
	}
	return []Item{{Kind: "user", Text: text, Images: images}}
}

func agentItem(text string) []Item {
	if text = strings.TrimSpace(text); text == "" {
		return nil
	}
	return []Item{{Kind: "agent", Text: text}}
}
