package predict

import "strings"

// editingKeys describes how a composer treats the readline-style editing
// keys, as recorded from the agent.
type editingKeys struct {
	// homeEndBuffer: Home and End go to the start and end of the whole draft
	// (OpenCode) instead of the logical line (Grok).
	homeEndBuffer bool
	// ctrlBF: Ctrl+B and Ctrl+F move by one character.
	ctrlBF bool
	// wrap lays out the draft at the composer width.
	wrap func([]Elem, int) []VisualRow
}

// edit predicts an editing key, or returns nil when the key is not an editing
// key or its outcome is not known from recordings (it then waits for the
// agent). Recorded on OpenCode 2.3.0 and Grok Build 1.0.77:
//
//   - Ctrl+A/Ctrl+E: start/end of the logical line.
//   - Ctrl+W, Alt+Backspace: delete the word before the cursor (spaces first).
//   - Alt+Left/Right, Esc b/f: to the start/end of the word.
//   - Ctrl+K/Ctrl+U: delete to the end/start of the logical line; at a line
//     break the result is not recorded.
//   - Up/Down: the same column one visual row up or down; from the first or
//     last row they recall history (not predicted).
//   - Ctrl+H: Backspace.
func (ek editingKeys) edit(k Key, st State, width int) EditFunc {
	switch k.Kind {
	case KeyHome, KeyEnd:
		end := k.Kind == KeyEnd
		if ek.homeEndBuffer {
			return func(s State) State {
				if end {
					return s.MoveTo(len(s.Elems))
				}
				return s.MoveTo(0)
			}
		}
		return lineEdge(end)
	case KeyAltBackspace:
		return deleteWordBefore(st)
	case KeyWordLeft:
		if !wordsAgree(st, false) {
			return nil
		}
		return func(s State) State { return s.MoveTo(s.WordStartBefore(IsAlnumWord)) }
	case KeyWordRight:
		if !wordsAgree(st, true) {
			return nil
		}
		return func(s State) State { return s.MoveTo(s.WordEndAfter(IsAlnumWord)) }
	case KeyUp, KeyDown:
		return ek.vertical(st, width, k.Kind == KeyDown)
	case KeyCtrl:
		switch k.Rune {
		case 'a':
			return lineEdge(false)
		case 'e':
			return lineEdge(true)
		case 'b', 'f':
			if !ek.ctrlBF {
				return nil
			}
			step := 1
			if k.Rune == 'b' {
				step = -1
			}
			return func(s State) State { return s.Move(step) }
		case 'h':
			return func(s State) State { return s.DeleteBackward() }
		case 'w':
			return deleteWordBefore(st)
		case 'k':
			if st.Cursor == st.LineEnd() && st.Cursor < len(st.Elems) {
				return nil // at a line break
			}
			return func(s State) State { return s.DeleteRange(s.Cursor, s.LineEnd()) }
		case 'u':
			if st.Cursor == st.LineStart() && st.Cursor > 0 {
				return nil // at a line break
			}
			return func(s State) State { return s.DeleteRange(s.LineStart(), s.Cursor) }
		}
	}
	return nil
}

func lineEdge(end bool) EditFunc {
	return func(s State) State {
		if end {
			return s.MoveTo(s.LineEnd())
		}
		return s.MoveTo(s.LineStart())
	}
}

// deleteWordBefore deletes the word before the cursor when letters-and-digits
// words and whitespace-separated words agree on its start (so punctuation,
// where agents differ, waits for the agent).
func deleteWordBefore(st State) EditFunc {
	if !wordsAgree(st, false) {
		return nil
	}
	return func(s State) State { return s.DeleteRange(s.WordStartBefore(IsAlnumWord), s.Cursor) }
}

func wordsAgree(st State, after bool) bool {
	if after {
		return st.WordEndAfter(IsAlnumWord) == st.WordEndAfter(IsNonSpaceWord)
	}
	return st.WordStartBefore(IsAlnumWord) == st.WordStartBefore(IsNonSpaceWord)
}

// vertical moves to the same column on the visual row above or below.
func (ek editingKeys) vertical(st State, width int, down bool) EditFunc {
	step := -1
	if down {
		step = 1
	}
	rows := ek.wrap(st.Elems, width)
	row, _ := cursorRowCol(st.Elems, rows, st.Cursor, width)
	if st.Empty() || row+step < 0 || row+step >= len(rows) {
		return nil // history
	}
	return func(s State) State {
		rows := ek.wrap(s.Elems, width)
		row, col := cursorRowCol(s.Elems, rows, s.Cursor, width)
		if row+step < 0 || row+step >= len(rows) {
			return s
		}
		return s.MoveTo(columnIn(s, rows[row+step], col))
	}
}

// slashCommand: the draft is a slash command being typed ("/" and no space).
func slashCommand(st State) bool {
	t := st.Text()
	return strings.HasPrefix(t, "/") && !strings.ContainsAny(t, " \n")
}

// menuOpen: a command or mention popup may be open, so Tab, Up, Down and
// Enter act on the popup: the draft is a slash command, or the word before
// the cursor starts with "@".
func menuOpen(st State) bool {
	if slashCommand(st) {
		return true
	}
	i := st.Cursor
	for i > 0 && !isSpaceElem(st.Elems[i-1]) {
		i--
	}
	return i < st.Cursor && st.Elems[i].S == "@"
}

// columnIn finds the element index in a visual row at col, after the row's
// hidden leading spaces.
func columnIn(st State, row VisualRow, col int) int {
	x := 0
	for i := row.Start + row.Hidden; i < row.End; i++ {
		if st.Elems[i].S == "\n" {
			return i
		}
		w := elemWidth(st.Elems[i])
		if x+w > col {
			return i
		}
		x += w
	}
	return row.End
}
