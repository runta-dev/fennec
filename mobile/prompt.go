package fennecmobile

import (
	"encoding/json"
	"errors"
	"fennec/internal/cloudops"
	"regexp"
	"strings"
)

// AgentPrompt is a question an agent is waiting on, read from its screen:
// an approval ("Do you want to proceed?") or a choice, with numbered options.
type AgentPrompt struct {
	Question string         `json:"question"`
	Detail   string         `json:"detail,omitempty"`
	Options  []PromptOption `json:"options"`
}

type PromptOption struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

var (
	promptOption  = regexp.MustCompile(`^(?:[❯›>▶→●]\s*)?([1-9])[.)]\s+(.+?)$`)
	promptKeyHint = regexp.MustCompile(`\s+\((?:[a-z]|esc|enter|tab|shift\+tab)\)$`)
	promptBorder  = regexp.MustCompile(`^[╭╰┌└├─━═┄┈\-\s╮╯┐┘┤]+$`)
	promptKey     = regexp.MustCompile(`^(?:[1-9]|esc)$`)
)

// promptLine drops the frame some agents draw around a prompt.
func promptLine(line string) string {
	line = strings.TrimRight(line, " \t\r")
	line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│┃║|"))
	return line
}

// ParsePrompt finds the numbered menu nearest the bottom of a screen and the
// question above it. Agents differ in framing and markers but all draw an
// approval or a question as "1. …", "2. …" under the question, which is all
// this relies on. ok is false when the screen has no such menu.
func ParsePrompt(screen string) (AgentPrompt, bool) {
	raw := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	lines := make([]string, len(raw))
	for i, line := range raw {
		lines[i] = promptLine(line)
	}
	// The menu's last option: the lowest option line among the last 20
	// non-empty lines (footers such as "Esc to cancel" may follow it).
	last, seen := -1, 0
	for i := len(lines) - 1; i >= 0 && seen < 20; i-- {
		if lines[i] == "" {
			continue
		}
		seen++
		if promptOption.MatchString(lines[i]) {
			last = i
			break
		}
	}
	if last < 0 {
		return AgentPrompt{}, false
	}
	// Walk up through the options; a non-option line between two options
	// continues the one above it (a wrapped title).
	type found struct {
		index int
		key   string
		title string
	}
	var options []found
	var wrapped []string
	for i := last; i >= 0 && last-i <= 40; i-- {
		line := lines[i]
		if m := promptOption.FindStringSubmatch(line); m != nil {
			title := m[2]
			if len(wrapped) > 0 {
				title += " " + strings.Join(wrapped, " ")
				wrapped = nil
			}
			options = append(options, found{i, m[1], title})
			if m[1] == "1" {
				break
			}
			continue
		}
		if line == "" || len(options) == 0 {
			break
		}
		wrapped = append([]string{line}, wrapped...)
	}
	if len(options) < 2 || options[len(options)-1].key != "1" {
		return AgentPrompt{}, false
	}
	prompt := AgentPrompt{}
	for i := len(options) - 1; i >= 0; i-- {
		want := string(rune('1' + len(options) - 1 - i))
		if options[i].key != want {
			return AgentPrompt{}, false
		}
		title := strings.TrimSpace(promptKeyHint.ReplaceAllString(options[i].title, ""))
		prompt.Options = append(prompt.Options, PromptOption{Key: options[i].key, Title: title})
	}
	// Above the menu: the question and what it is about (a command, a file),
	// up to a frame edge or a gap of two blank lines.
	var context []string
	blank := 0
	for i := options[len(options)-1].index - 1; i >= 0 && len(context) < 16; i-- {
		line := lines[i]
		if promptBorder.MatchString(line) && strings.TrimSpace(line) != "" {
			break
		}
		if line == "" {
			blank++
			if blank == 2 && len(context) > 0 {
				break
			}
			context = append([]string{""}, context...)
			continue
		}
		blank = 0
		context = append([]string{line}, context...)
	}
	question := -1
	for i := len(context) - 1; i >= 0; i-- {
		if strings.HasSuffix(context[i], "?") {
			question = i
			break
		}
	}
	// What the question is about sits between it and the menu (Codex) or,
	// when nothing does, above it (Claude Code's framed prompt).
	detail := context
	if question >= 0 {
		prompt.Question = context[question]
		detail = context[question+1:]
		if strings.TrimSpace(strings.Join(detail, "")) == "" {
			detail = context[:question]
		}
	}
	prompt.Detail = strings.TrimSpace(strings.Join(collapseBlank(detail), "\n"))
	return prompt, true
}

func collapseBlank(lines []string) []string {
	var out []string
	for _, line := range lines {
		if line == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Prompt reads the session's screen and returns the question it is waiting
// on as JSON (AgentPrompt), or "{}" when its screen shows none.
func (s *Sessions) Prompt(address, sessionID string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	if !addressPattern.MatchString(address) || !sessionPattern.MatchString(sessionID) {
		return "", errors.New("invalid terminal")
	}
	raw, _, err := s.runRead(func(string) (string, error) { return cloudops.ReadScript(address, sessionID) })
	if err != nil {
		return "", err
	}
	var read struct {
		Screen struct {
			Text string `json:"text"`
		} `json:"screen"`
	}
	if err := json.Unmarshal(raw, &read); err != nil {
		return "", errors.New("invalid terminal screen")
	}
	prompt, ok := ParsePrompt(read.Screen.Text)
	if !ok {
		return "{}", nil
	}
	data, err := json.Marshal(prompt)
	return string(data), err
}

// Answer presses one key in the session (an option's number, or esc), only
// if the terminal still belongs to that session. It never types text or
// presses Enter, so it can't submit anything but the chosen option.
func (s *Sessions) Answer(address, sessionID, key string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	if !addressPattern.MatchString(address) || !sessionPattern.MatchString(sessionID) || !promptKey.MatchString(key) {
		return "", errors.New("invalid answer")
	}
	herdr := `"$HOME/.local/bin/herdr-core"`
	script := herdr + ` get ` + address + ` --json | grep -Eq '"fennec\.session"[[:space:]]*:[[:space:]]*"` + sessionID + `"' || { echo 'session moved' >&2; exit 1; }; ` +
		herdr + ` keys --json ` + address + ` ` + key
	raw, _, err := s.run(func(string) (string, error) { return script, nil })
	return string(raw), err
}
