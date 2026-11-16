package predict

import (
	"strings"
	"unicode"
)

// VisualRow is one wrapped screen row of a composer: the half-open range of
// element indexes it shows and their total width.
type VisualRow struct {
	Start, End int
	Width      int
	// Hidden counts elements at the start of the row that are not drawn
	// (leading whitespace of a soft-wrapped row, for renderers that trim it).
	Hidden int
}

// elemWidth is the display width of one element.
func elemWidth(e Elem) int {
	if e.S == "\n" {
		return 0
	}
	return StringWidth(e.S)
}

// logicalLines splits elements at newlines; each line keeps its absolute
// start index.
func logicalLines(elems []Elem) [][2]int {
	var lines [][2]int
	start := 0
	for i, e := range elems {
		if e.S == "\n" {
			lines = append(lines, [2]int{start, i})
			start = i + 1
		}
	}
	return append(lines, [2]int{start, len(elems)})
}

// WrapAnsi reproduces wrap-ansi with {hard: true, trim: false}, which Ink
// uses for Claude Code's prompt: words are split on single spaces, a space
// before a wrapped word stays at the end of the previous row, and words longer
// than the width are broken by character. Tokens are treated as text.
func WrapAnsi(elems []Elem, width int) []VisualRow {
	width = max(1, width)
	var rows []VisualRow
	for _, line := range logicalLines(elems) {
		rows = append(rows, wrapAnsiLine(elems, line[0], line[1], width)...)
	}
	return rows
}

func wrapAnsiLine(elems []Elem, start, end, width int) []VisualRow {
	// Expand tokens into their characters so they wrap like the text Ink sees.
	type unit struct {
		index int // element index this character belongs to
		w     int
		space bool
	}
	var units []unit
	for i := start; i < end; i++ {
		e := elems[i]
		if e.Token {
			for _, r := range e.S {
				units = append(units, unit{index: i, w: RuneWidth(r), space: r == ' '})
			}
			continue
		}
		units = append(units, unit{index: i, w: elemWidth(e), space: e.S == " "})
	}
	// Split into words on spaces (each space separates; consecutive spaces
	// yield empty words), as String.split(' ') does.
	var words [][]unit
	current := []unit{}
	var separators []unit
	for _, u := range units {
		if u.space {
			words = append(words, current)
			separators = append(separators, u)
			current = []unit{}
			continue
		}
		current = append(current, u)
	}
	words = append(words, current)
	rows := [][]unit{{}}
	rowWidth := func() int {
		n := 0
		for _, u := range rows[len(rows)-1] {
			n += u.w
		}
		return n
	}
	wrapWord := func(word []unit) {
		visible := rowWidth()
		for i, u := range word {
			if visible+u.w <= width {
				rows[len(rows)-1] = append(rows[len(rows)-1], u)
			} else {
				rows = append(rows, []unit{u})
				visible = 0
			}
			visible += u.w
			if visible == width && i < len(word)-1 {
				rows = append(rows, []unit{})
				visible = 0
			}
		}
		if visible == 0 && len(rows[len(rows)-1]) > 0 && len(rows) > 1 {
			last := rows[len(rows)-1]
			rows = rows[:len(rows)-1]
			rows[len(rows)-1] = append(rows[len(rows)-1], last...)
		}
	}
	for index, word := range words {
		length := 0
		for _, u := range word {
			length += u.w
		}
		rw := rowWidth()
		if index != 0 {
			if rw >= width {
				rows = append(rows, []unit{})
				rw = 0
			}
			rows[len(rows)-1] = append(rows[len(rows)-1], separators[index-1])
			rw++
		}
		if length > width {
			remaining := width - rw
			thisLine := 1 + (length-remaining-1)/width
			nextLine := (length - 1) / width
			if nextLine < thisLine {
				rows = append(rows, []unit{})
			}
			wrapWord(word)
			continue
		}
		if rw+length > width && rw > 0 && length > 0 {
			rows = append(rows, []unit{})
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], word...)
	}
	// Convert character rows back to element ranges. A token never splits in
	// practice (they are shorter than any composer); if one does, its row
	// boundary falls at the token start.
	out := make([]VisualRow, 0, len(rows))
	pos := start
	for _, row := range rows {
		vr := VisualRow{Start: pos, End: pos}
		seen := map[int]bool{}
		for _, u := range row {
			vr.Width += u.w
			if !seen[u.index] {
				seen[u.index] = true
				if u.index >= vr.End {
					vr.End = u.index + 1
				}
			}
		}
		if vr.End < vr.Start {
			vr.End = vr.Start
		}
		pos = vr.End
		out = append(out, vr)
	}
	if len(out) > 0 {
		out[len(out)-1].End = end
	}
	return out
}

// isBreakableSpace matches the whitespace Codex lets hang past the edge.
func isBreakableSpace(s string) bool {
	for _, r := range s {
		return unicode.IsSpace(r) && r != '\u00a0' && r != '\u2007' && r != '\u202f' && r != '\n'
	}
	return false
}

// WrapCodex reproduces the Codex composer wrapping: first-fit word wrapping
// where interior spaces hang past the edge, a word that does not fit moves to
// the next row, overlong words break at the grapheme that overflows, wide
// (CJK) characters are break opportunities, and a logical line whose width is
// at least the wrap width gets an extra empty insertion row.
func WrapCodex(elems []Elem, width int) []VisualRow {
	width = max(1, width)
	var rows []VisualRow
	for _, line := range logicalLines(elems) {
		rows = append(rows, wrapCodexLine(elems, line[0], line[1], width)...)
	}
	return rows
}

func wrapCodexLine(elems []Elem, start, end, width int) []VisualRow {
	// Trimmed content end: spaces after it never hang (they wrap).
	contentEnd := end
	for contentEnd > start && isBreakableSpace(elems[contentEnd-1].S) {
		contentEnd--
	}
	var rows []VisualRow
	row := VisualRow{Start: start, End: start}
	lineWidth := 0
	breakable := func(i int) bool { // may a row start at element i?
		if i <= start {
			return false
		}
		prev, cur := elems[i-1], elems[i]
		if isBreakableSpace(prev.S) && !isBreakableSpace(cur.S) {
			return true
		}
		if elemWidth(prev) == 2 || elemWidth(cur) == 2 {
			// Not before closing or after opening punctuation: Codex keeps
			// "，" with the character before it (recorded on 0.169.0).
			return !strings.ContainsRune(cjkClosing, firstRune(cur.S)) && !strings.ContainsRune(cjkOpening, firstRune(prev.S))
		}
		return strings.HasSuffix(prev.S, "-") && !isBreakableSpace(cur.S)
	}
	i := start
	for i < end {
		e := elems[i]
		w := elemWidth(e)
		if isBreakableSpace(e.S) && i < contentEnd {
			// Interior whitespace hangs on the current row.
			row.End, row.Width = i+1, row.Width+w
			i++
			continue
		}
		if row.Width+w > width && row.Width > 0 {
			// Move the whole word if it started after a break opportunity on
			// this row; otherwise break here.
			wordStart := i
			for wordStart > row.Start && !breakable(wordStart) {
				wordStart--
			}
			if wordStart > row.Start {
				ww := 0
				for j := row.Start; j < wordStart; j++ {
					ww += elemWidth(elems[j])
				}
				rows = append(rows, VisualRow{Start: row.Start, End: wordStart, Width: ww})
				row = VisualRow{Start: wordStart, End: wordStart}
				for j := wordStart; j < i; j++ {
					row.Width += elemWidth(elems[j])
				}
				row.End = i
			} else {
				rows = append(rows, row)
				row = VisualRow{Start: i, End: i}
			}
		}
		row.End, row.Width = i+1, row.Width+w
		i++
	}
	rows = append(rows, row)
	if row.End != end {
		rows[len(rows)-1].End = end
	}
	total := 0
	for j := start; j < end; j++ {
		if !isBreakableSpace(elems[j].S) || j < contentEnd {
			total += elemWidth(elems[j])
		}
	}
	lineWidth = total
	if lineWidth >= width && rows[len(rows)-1].Width >= width {
		rows = append(rows, VisualRow{Start: end, End: end})
	}
	return rows
}

// cursorRowCol maps an element index to a row and column within rows, given
// whether the cursor at the end of a full row moves to the next row.
func cursorRowCol(elems []Elem, rows []VisualRow, cursor, width int) (int, int) {
	row := 0
	for i, r := range rows {
		if r.Start <= cursor {
			row = i
		}
	}
	// A newline ends its row; a cursor just after it sits on the next row,
	// which the loop above already chose because that row starts there.
	col := 0
	for j := rows[row].Start + rows[row].Hidden; j < cursor && j < len(elems); j++ {
		col += elemWidth(elems[j])
	}
	return row, col
}
