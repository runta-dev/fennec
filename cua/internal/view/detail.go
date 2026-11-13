package view

import (
	"strings"
	"unicode"
)

// SecondaryActions turns an element's accessibility action names into the
// names an agent sees: "AXShowMenu" becomes "Show Menu", a custom action
// ("Name:Delete\nTarget:...") its name. The press is left out: it is what a
// click does.
func SecondaryActions(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if n == "AXPress" {
			continue
		}
		s := SpokenAction(n)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SpokenAction is the agent-facing name of one action name.
func SpokenAction(name string) string {
	if rest, ok := strings.CutPrefix(name, "Name:"); ok {
		line, _, _ := strings.Cut(rest, "\n")
		return strings.TrimSpace(line)
	}
	name = strings.TrimPrefix(name, "AX")
	var b strings.Builder
	rs := []rune(name)
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// ActionName maps an agent-facing action name back to one of the element's
// raw action names; ok is false when the element lists no such action.
func ActionName(raw []string, want string) (string, bool) {
	w := strings.TrimSpace(want)
	for _, n := range raw {
		if n == w || strings.EqualFold(SpokenAction(n), w) {
			return n, true
		}
	}
	return "", false
}
