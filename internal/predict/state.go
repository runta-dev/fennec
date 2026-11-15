package predict

import (
	"strings"
	"unicode"
)

// Elem is one editing unit of a composer: a grapheme, a newline, or an atomic
// token such as "[Image #2]".
type Elem struct {
	S     string
	Token bool
}

// State is a composer's text and insertion point (an index into Elems).
type State struct {
	Elems  []Elem
	Cursor int
	// Empty means the composer shows its placeholder.
	Mode string // agent-specific prompt mode, e.g. "" or "bash"
}

func (s State) Text() string {
	var b strings.Builder
	for _, e := range s.Elems {
		b.WriteString(e.S)
	}
	return b.String()
}

func (s State) Clone() State {
	return State{Elems: append([]Elem(nil), s.Elems...), Cursor: s.Cursor, Mode: s.Mode}
}

func (s State) Empty() bool { return len(s.Elems) == 0 }

// TextElems splits text into graphemes: combining marks join the previous
// element, newlines are their own elements.
func TextElems(text string) []Elem {
	var out []Elem
	for _, r := range text {
		if r == '\r' {
			continue
		}
		if RuneWidth(r) == 0 && r != '\n' && len(out) > 0 && out[len(out)-1].S != "\n" && !out[len(out)-1].Token {
			out[len(out)-1].S += string(r)
			continue
		}
		out = append(out, Elem{S: string(r)})
	}
	return out
}

func (s State) Insert(elems ...Elem) State {
	out := s.Clone()
	out.Elems = append(out.Elems[:s.Cursor], append(append([]Elem(nil), elems...), s.Elems[s.Cursor:]...)...)
	out.Cursor = s.Cursor + len(elems)
	return out
}

func (s State) DeleteBackward() State {
	if s.Cursor == 0 {
		return s
	}
	out := s.Clone()
	out.Elems = append(out.Elems[:s.Cursor-1], s.Elems[s.Cursor:]...)
	out.Cursor--
	return out
}

func (s State) DeleteForward() State {
	if s.Cursor >= len(s.Elems) {
		return s
	}
	out := s.Clone()
	out.Elems = append(out.Elems[:s.Cursor], s.Elems[s.Cursor+1:]...)
	return out
}

func (s State) Move(delta int) State {
	out := s.Clone()
	out.Cursor = max(0, min(len(s.Elems), s.Cursor+delta))
	return out
}

// LineStart and LineEnd are the bounds of the logical line around the cursor.
func (s State) LineStart() int {
	i := s.Cursor
	for i > 0 && s.Elems[i-1].S != "\n" {
		i--
	}
	return i
}

func (s State) LineEnd() int {
	i := s.Cursor
	for i < len(s.Elems) && s.Elems[i].S != "\n" {
		i++
	}
	return i
}

func (s State) MoveTo(i int) State {
	out := s.Clone()
	out.Cursor = max(0, min(len(s.Elems), i))
	return out
}

// DeleteRange removes Elems[from:to] and leaves the cursor at from.
func (s State) DeleteRange(from, to int) State {
	from, to = max(0, from), min(len(s.Elems), to)
	if from >= to {
		return s
	}
	out := s.Clone()
	out.Elems = append(out.Elems[:from], s.Elems[to:]...)
	out.Cursor = from
	return out
}

// WordStartBefore returns the start of the word before the cursor using the
// readline convention: skip separators, then word characters.
func (s State) WordStartBefore(isWord func(string) bool) int {
	i := s.Cursor
	for i > 0 && !isWord(s.Elems[i-1].S) && s.Elems[i-1].S != "\n" {
		i--
	}
	for i > 0 && isWord(s.Elems[i-1].S) {
		i--
	}
	return i
}

func (s State) WordEndAfter(isWord func(string) bool) int {
	i := s.Cursor
	for i < len(s.Elems) && !isWord(s.Elems[i].S) && s.Elems[i].S != "\n" {
		i++
	}
	for i < len(s.Elems) && isWord(s.Elems[i].S) {
		i++
	}
	return i
}

// IsAlnumWord treats letters, digits and underscore as word characters.
func IsAlnumWord(e string) bool {
	for _, r := range e {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	}
	return false
}

// IsNonSpaceWord treats everything but whitespace as word characters.
func IsNonSpaceWord(e string) bool {
	for _, r := range e {
		return !unicode.IsSpace(r)
	}
	return false
}

// Equal compares text and cursor exactly.
func (s State) Equal(o State) bool {
	if s.Cursor != o.Cursor || len(s.Elems) != len(o.Elems) || s.Mode != o.Mode {
		return false
	}
	for i := range s.Elems {
		if s.Elems[i] != o.Elems[i] {
			return false
		}
	}
	return true
}
