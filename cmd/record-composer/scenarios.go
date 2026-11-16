package main

import (
	"fmt"
	"strings"
)

// A step is one action in a scenario: typed text (one key every typingGap),
// raw key bytes, or a bracketed paste.
type step struct {
	typed  string
	commit string // sent in one write, as an input method commits a phrase
	keys   []byte
	paste  string
}

func typeText(s string) step { return step{typed: s} }
func commit(s string) step   { return step{commit: s} }
func key(b ...byte) step     { return step{keys: b} }
func paste(s string) step    { return step{paste: s} }

var (
	shiftEnter = key(0x1b, '[', '1', '3', ';', '2', 'u')
	backspace  = key(0x7f)
	left       = key(0x1b, '[', 'D')
	up         = key(0x1b, '[', 'A')
	down       = key(0x1b, '[', 'B')
	home       = key(0x1b, '[', 'H')
	end        = key(0x1b, '[', 'F')
	escape     = key(0x1b)
	tab        = key('\t')
)

func ctrl(c byte) step { return key(c & 0x1f) }

func repeat(n int, s step) []step {
	out := make([]step, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// clear empties a draft of a few lines (Ctrl+U and Backspace per line).
func clear() []step {
	var out []step
	for range 4 {
		out = append(out, ctrl('u'), backspace)
	}
	return out
}

// scenarios never submit: no step sends Enter (validated in checkSafe), and
// keys that submit a draft on some agent are left out for that agent.
var scenarios = map[string]func(agent string) []step{
	// typing: words that wrap twice, Shift+Enter, Backspace, Left with
	// insertion and a short inline paste (the original recordings).
	"typing": func(string) []step {
		s := []step{
			typeText("this is a long line typed into the composer so that it has to wrap the text onto another"),
			typeText(" row and keep going until it needs a third row of text as well so that growth happens twice"),
			shiftEnter, typeText("line two"),
		}
		s = append(s, repeat(3, backspace)...)
		s = append(s, repeat(2, left)...)
		return append(s, typeText("X"), paste(" pasted words"))
	},
	// edit: readline editing keys on two lines and on a wrapped line.
	"edit": func(string) []step {
		s := []step{typeText("alpha beta gamma"), shiftEnter, typeText("delta epsilon zeta"),
			ctrl('a'), typeText("A"), ctrl('e'), typeText("E"), home, typeText("H"), end, typeText("N"),
			ctrl('w'), key(0x1b, 0x7f), ctrl('b'), ctrl('b'), typeText("B"), ctrl('f'), typeText("F"),
			up, typeText("U"), down, typeText("D"),
			key(0x1b, '[', '1', ';', '3', 'D'), typeText("W"), key(0x1b, '[', '1', ';', '3', 'C'), typeText("R"),
			key(0x1b, 'b'), typeText("b"), key(0x1b, 'f'), typeText("f"),
			left, left, left, ctrl('k'), ctrl('u'), ctrl('h'), key(0x1b, '[', '3', '~'),
			shiftEnter,
			typeText("a wrapped line that is long enough to need two rows in the composer of every agent we model here, and more words to be sure"),
			ctrl('a'), typeText("1"), ctrl('e'), typeText("2"), home, typeText("3"), end, typeText("4"),
			up, typeText("5"), down, typeText("6"), ctrl('k'), ctrl('u'),
		}
		return s
	},
	// menu: the "/" and "@" popups, Esc, Tab and history on an empty draft.
	"menu": func(agent string) []step {
		s := []step{typeText("/"), typeText("he"), backspace, backspace, backspace,
			typeText("/mo"), escape, ctrl('u'), backspace, backspace, backspace,
			typeText("@"), typeText("re"), backspace, backspace, backspace,
			typeText("see @"), typeText("x"), escape, typeText(" done"), ctrl('u'),
			tab, tab}
		if agent != "codex" { // Codex may submit a draft on Tab
			s = append(s, typeText("draft"), tab, ctrl('u'))
		}
		return append(s, up, down)
	},
	// paste: 2 to 10 lines with and without a trailing break, a blank line
	// inside, long single lines, and a paste between words.
	"paste": func(string) []step {
		lines := func(prefix string, n int, trailing bool) string {
			var b strings.Builder
			for i := 1; i <= n; i++ {
				if i > 1 {
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "%s%d", prefix, i)
			}
			if trailing {
				b.WriteByte('\n')
			}
			return b.String()
		}
		var s []step
		for _, p := range []string{lines("a", 2, false), lines("b", 3, false), lines("c", 3, true), lines("d", 4, false),
			lines("e", 10, false), lines("f", 10, true), "g1\n\ng3", strings.Repeat("word ", 40), strings.Repeat("longer ", 115)} {
			s = append(s, paste(p))
			s = append(s, clear()...)
		}
		s = append(s, typeText("before "), paste(lines("h", 5, false)), typeText("after"))
		s = append(s, repeat(7, backspace)...)
		return append(s, clear()...)
	},
}

// Chinese words for the cjk scenario (escaped: sources stay English).
const (
	cjkHello   = "\u4f60\u597d"       // hello
	cjkWorld   = "\u4e16\u754c"       // world
	cjkTest    = "\u6d4b\u8bd5"       // test
	cjkChinese = "\u4e2d\u6587"       // Chinese
	cjkIME     = "\u8f93\u5165\u6cd5" // input method
	cjkPredict = "\u9884\u6d4b"       // predict
	cjkEcho    = "\u56de\u663e"       // echo
	cjkInsert  = "\u63d2\u5165"       // insert
	cjkOf      = "\u7684"             // (particle)
	cjkComma   = "\uff0c"             // full-width comma
	cjkStop    = "\u3002"             // full-width full stop
)

func init() {
	// cjk: phrases committed by an input method (one write each), wide
	// characters wrapping at the row end (a wide character never splits, so
	// a row may end one column short), mixed with ASCII words, full-width
	// punctuation, Backspace and arrows over wide characters, Shift+Enter,
	// and a pasted Chinese sentence.
	scenarios["cjk"] = func(string) []step {
		phrases := []string{cjkHello, cjkWorld, cjkComma, cjkTest, cjkChinese, cjkIME, cjkOf, cjkPredict, cjkEcho, cjkStop}
		var s []step
		for range 4 { // about 80 columns per round: wraps at every size
			for _, p := range phrases {
				s = append(s, commit(p))
			}
			s = append(s, typeText(" fennec "))
		}
		s = append(s, repeat(3, backspace)...)
		s = append(s, left, left, commit(cjkInsert), key(0x1b, '[', 'C'), commit(cjkOf))
		s = append(s, shiftEnter, typeText("mixed "), commit(cjkChinese+cjkTest), typeText(" text "), commit(cjkPredict))
		s = append(s, backspace, backspace, paste(cjkHello+cjkComma+cjkWorld+cjkStop+" ok"))
		return append(s, clear()...)
	}
}

// checkSafe rejects a scenario that could submit a prompt (and so use
// inference): Enter, or a line break typed outside a paste.
func checkSafe(steps []step) error {
	for i, s := range steps {
		for _, b := range s.keys {
			if b == '\r' || b == '\n' {
				return fmt.Errorf("step %d sends a line break", i)
			}
		}
		if strings.ContainsAny(s.typed+s.commit, "\r\n\x1b") {
			return fmt.Errorf("step %d types a line break", i)
		}
		if strings.Contains(s.paste, "\r") || strings.Contains(s.paste, "\x1b") {
			return fmt.Errorf("step %d pastes control characters", i)
		}
	}
	return nil
}
