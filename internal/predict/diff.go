package predict

import (
	"bytes"
	"fmt"
)

// DiffScreen returns bytes that turn a terminal showing from into one showing
// to: the changed cells, then the scroll region, pen, cursor position and
// cursor visibility of to, inside one synchronized-output frame. Cells keep
// their OSC 8 hyperlinks, and the open hyperlink is to's. full
// repaints every cell (when what the terminal shows may differ from from).
//
// It only handles what it can reproduce exactly and returns false otherwise:
// both screens on the alternate screen (no scrollback to keep), the same
// size, complete (not inside an escape sequence or a synchronized frame),
// plain character sets, no origin or insert mode, autowrap on, and no pending
// wrap in to. The result is checked by replaying it onto a copy of from.
func DiffScreen(from, to *Screen, full bool) ([]byte, bool) {
	if !diffable(from) || !diffable(to) || from.Rows != to.Rows || from.Cols != to.Cols || to.PendingWrap {
		return nil, false
	}
	var b bytes.Buffer
	b.WriteString("\x1b[?2026h")
	pen, penKnown := Attr{}, false
	setPen := func(a Attr) {
		if !penKnown || a != pen {
			b.WriteString(SGR(a))
			pen, penKnown = a, true
		}
	}
	// The hyperlink the terminal prints with: from's, until changed.
	link := from.PenLink
	setLink := func(l string) {
		if l != link {
			b.WriteString(OSC8(l))
			link = l
		}
	}
	for r := 0; r < to.Rows; r++ {
		want, have := to.grid[r], from.grid[r]
		for c := 0; c < to.Cols; {
			if !full && want[c] == have[c] && (c+1 >= to.Cols || want[c+1].Width != 0 || want[c+1] == have[c+1]) {
				c++
				continue
			}
			start := c
			if start > 0 && want[start].Width == 0 {
				start-- // the left half of a wide character
			}
			fmt.Fprintf(&b, "\x1b[%d;%dH", r+1, start+1)
			end := start
			for end < to.Cols && (full || want[end] != have[end] || end+1 < to.Cols && want[end+1].Width == 0 && want[end+1] != have[end+1]) {
				end++
			}
			if end < to.Cols && want[end].Width == 0 {
				end++ // finish a wide character
			}
			for col := start; col < end; col++ {
				cell := want[col]
				if cell.Width == 0 {
					continue
				}
				setPen(cell.Attr)
				setLink(cell.Link)
				b.WriteString(cell.Glyph())
			}
			c = max(end, c+1)
		}
	}
	if from.Top != to.Top || from.Bottom != to.Bottom {
		fmt.Fprintf(&b, "\x1b[%d;%dr", to.Top+1, to.Bottom+1) // homes the cursor
	}
	b.WriteString(SGR(to.Pen))
	setLink(to.PenLink)
	fmt.Fprintf(&b, "\x1b[%d;%dH", to.Row+1, to.Col+1)
	if to.CursorVisible {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	b.WriteString("\x1b[?2026l")
	out := b.Bytes()
	check := from.Clone()
	check.Write(out)
	if ok, _ := SameScreen(check, to); !ok {
		return nil, false
	}
	return out, true
}

func diffable(s *Screen) bool {
	return s.AltActive && s.Ground() && !s.Sync && !s.OriginMode && !s.InsertMode && s.AutoWrap &&
		!s.G0Graphics && !s.G1Graphics && !s.ShiftOut
}

// SameScreen compares what two screens show and the state later output
// depends on: every cell (with its hyperlink), the cursor and its visibility,
// pending wrap, pen and open hyperlink, scroll region and modes.
func SameScreen(a, b *Screen) (bool, string) {
	if a.Rows != b.Rows || a.Cols != b.Cols {
		return false, "size"
	}
	for r := 0; r < a.Rows; r++ {
		for c := 0; c < a.Cols; c++ {
			if a.grid[r][c] != b.grid[r][c] {
				return false, fmt.Sprintf("cell %d,%d: %+v vs %+v", r, c, a.grid[r][c], b.grid[r][c])
			}
		}
	}
	switch {
	case a.Row != b.Row || a.Col != b.Col:
		return false, fmt.Sprintf("cursor %d,%d vs %d,%d", a.Row, a.Col, b.Row, b.Col)
	case a.CursorVisible != b.CursorVisible:
		return false, "cursor visibility"
	case a.PendingWrap != b.PendingWrap:
		return false, "pending wrap"
	case a.Pen != b.Pen || a.PenLink != b.PenLink:
		return false, "pen"
	case a.Top != b.Top || a.Bottom != b.Bottom:
		return false, "scroll region"
	case a.AltActive != b.AltActive || a.OriginMode != b.OriginMode || a.AutoWrap != b.AutoWrap || a.InsertMode != b.InsertMode:
		return false, "modes"
	}
	return true, ""
}

// OSC8 opens the hyperlink link ("params;URI"), or closes it when empty.
func OSC8(link string) string {
	if link == "" {
		return "\x1b]8;;\x1b\\"
	}
	return "\x1b]8;" + link + "\x1b\\"
}
