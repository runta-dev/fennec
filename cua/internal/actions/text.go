package actions

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// protocolTags are the closing tags of agent tool-call markup that can leak
// into the end of a typed text.
var protocolTags = map[string]bool{
	"text": true, "parameter": true, "invoke": true, "function_calls": true,
	"function_call": true, "tool_use": true, "tool_call": true,
}

var closingLine = regexp.MustCompile(`^\s*</([A-Za-z_][\w.-]*:)?([A-Za-z_][\w-]*)>\s*$`)

// StripProtocolTail drops trailing lines that hold only a closing tag of
// agent tool-call markup (namespaced variants included) when the text never
// opened that tag. A tag the text opened, a closing tag inside a line and
// ordinary HTML closers are kept.
func StripProtocolTail(text string) string {
	lines := strings.Split(text, "\n")
	cut := -1
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := closingLine.FindStringSubmatch(line)
		if m == nil || !protocolTags[m[2]] {
			break
		}
		if strings.Contains(strings.Join(lines[:i], "\n"), "<"+m[1]+m[2]) {
			break
		}
		cut = i
	}
	if cut < 0 {
		return text
	}
	return strings.Join(lines[:cut], "\n")
}

// TypedTail is what a field must end with after typing: the last 16
// characters after the final line break, or nothing when the text ends in a
// line break.
func TypedTail(text string) string {
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = text[i+1:]
	}
	r := []rune(text)
	if len(r) > 16 {
		r = r[len(r)-16:]
	}
	return string(r)
}

// Chunks splits text into consecutive pieces that each hold at most 8
// distinct non-ASCII characters; together they equal the text.
func Chunks(text string) []string {
	var out []string
	start := 0
	seen := map[rune]bool{}
	for i, r := range text {
		if r < utf8.RuneSelf || seen[r] {
			continue
		}
		if len(seen) == 8 {
			out = append(out, text[start:i])
			start = i
			seen = map[rune]bool{}
		}
		seen[r] = true
	}
	if start < len(text) || len(out) == 0 {
		out = append(out, text[start:])
	}
	return out
}

// BMP says whether every character lies in the Basic Multilingual Plane.
func BMP(text string) bool {
	for _, r := range text {
		if r > 0xFFFF {
			return false
		}
	}
	return true
}

// unwrap removes line breaks a terminal inserted to wrap long lines: the
// screen read joins its rows.
func unwrap(screen string) string {
	return strings.NewReplacer("\r\n", "", "\n", "", "\r", "").Replace(screen)
}

// TerminalShows says whether a terminal screen shows typed text exactly,
// spaces and letter case intact; only wrapping line breaks are ignored, and
// trailing spaces or tabs of the typed text need not appear.
func TerminalShows(screen, typed string) bool {
	return CountCopies(screen, typed) > 0
}

// CountCopies counts the copies of typed text on a terminal screen, under
// the rules of TerminalShows.
func CountCopies(screen, typed string) int {
	want := strings.TrimRight(strings.ReplaceAll(typed, "\n", ""), " \t")
	if want == "" {
		return 0
	}
	return strings.Count(unwrap(screen), want)
}
