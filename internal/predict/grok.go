package predict

import (
	"regexp"
	"strings"
)

// Grok Build's composer (ratatui, alternate screen, whole screen painted with
// the app background), recorded at 100x30:
//
//	╭──────────────────────────────╮
//	│ ❯ text…                      │   ❯ at column 4, text from column 6
//	│   continuation               │
//	╰──────── model · mode ────────╯   bottom border never moves
//	                                     blank row
//	footer: a notice while the draft is empty, key hints once it is not
//
// Text wraps at columns-10 like wrap-ansi, and a space that wraps after an
// exactly full row is not drawn. The box grows upward into the blank rows
// above it. Once a [Pasted: N lines] chip is in the draft Grok switches to a
// different wrapper and opens an expand popup, so such drafts are not locked.
const (
	grokTextCol = 6
	grokLeft    = 2
)

var grokChipRe = regexp.MustCompile(`\[Pasted: \d+ lines?\]`)

type grokBox struct {
	promptRow, lastRow int
	right              int // column of the right border
	width              int
	topBorder          []Cell
	promptRowCells     []Cell
	blank              Attr // a blank text-area cell
	screenBG           Attr
	footer             [][]Cell // rows below the bottom border
}

type grokProfile struct {
	width, rows int
	// footers as last seen with an empty and with a non-empty draft
	emptyFooter, typingFooter [][]Cell
}

func newGrokProfile() *grokProfile { return &grokProfile{} }

func (p *grokProfile) Name() string { return "grok" }

func (p *grokProfile) maxRows(rows int) int { return max(1, rows/2-4) }

func (p *grokProfile) geometry(s *Screen) (grokBox, bool) {
	fail := func() (grokBox, bool) { return grokBox{}, false }
	// Grok hides the cursor at the end of an exactly full row; elsewhere a
	// hidden cursor means the composer is not in use.
	if (!s.CursorVisible && s.Col != s.Cols-4) || s.Cols < 20 {
		return fail()
	}
	right := s.Cols - 3
	inBox := func(r int) bool {
		return s.Cell(r, grokLeft).Text == "│" && s.Cell(r, right).Text == "│"
	}
	r := s.Row
	if !inBox(r) {
		return fail()
	}
	for r >= 0 && inBox(r) && s.Cell(r, 4).Text != "❯" {
		if !s.Cell(r, 4).Blank() {
			return fail()
		}
		r--
	}
	if r < 1 || !inBox(r) || s.Cell(r, 4).Text != "❯" || s.Cell(r-1, grokLeft).Text != "╭" || s.Cell(r-1, right).Text != "╮" {
		return fail()
	}
	box := grokBox{promptRow: r, lastRow: r, right: right, width: right - 1 - grokTextCol}
	for box.lastRow+1 < s.Rows && inBox(box.lastRow+1) {
		box.lastRow++
		if !s.Cell(box.lastRow, 4).Blank() {
			return fail()
		}
	}
	bottom := box.lastRow + 1
	if s.Cell(bottom, grokLeft).Text != "╰" || s.Cell(bottom, right).Text != "╯" {
		return fail()
	}
	if box.lastRow-box.promptRow+1 > p.maxRows(s.Rows) || s.Row > box.lastRow || s.Col < grokTextCol || s.Col > right-1 {
		return fail()
	}
	for row := box.promptRow; row <= box.lastRow; row++ {
		// Column 3, 5 and the column before the border stay blank; anything
		// there is a scrollbar or a layout this profile does not know.
		if !s.Cell(row, 3).Blank() || !s.Cell(row, 5).Blank() || !s.Cell(row, right-1).Blank() {
			return fail()
		}
	}
	box.topBorder = copyRow(s, r-1)
	box.promptRowCells = copyRow(s, r)
	box.blank = s.Cell(r, right-1).Attr
	box.screenBG = Attr{BG: s.Cell(r-1, 0).Attr.BG}
	for row := bottom + 1; row < s.Rows; row++ {
		box.footer = append(box.footer, copyRow(s, row))
	}
	p.width, p.rows = box.width, s.Rows
	return box, true
}

func (p *grokProfile) remember(box grokBox, st State) {
	if st.Empty() {
		p.emptyFooter = box.footer
	} else {
		p.typingFooter = box.footer
	}
}

func (p *grokProfile) Locate(s *Screen) (Lock, bool) {
	box, ok := p.geometry(s)
	if !ok {
		return Lock{}, false
	}
	st, ok := p.parse(s, box)
	if !ok {
		return Lock{}, false
	}
	p.remember(box, st)
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

func (p *grokProfile) Match(s *Screen, st State) (Lock, bool) {
	box, ok := p.geometry(s)
	if !ok || st.Empty() || grokChipRe.MatchString(st.Text()) {
		return Lock{}, false
	}
	rows := grokWrap(st.Elems, box.width)
	if len(rows) != box.lastRow-box.promptRow+1 {
		return Lock{}, false
	}
	for i, text := range layoutStrings(st.Elems, rows) {
		if got, _ := rowRun(s, box.promptRow+i, grokTextCol, grokTextCol+box.width); got != text {
			return Lock{}, false
		}
	}
	if row, col := grokCursor(st.Elems, rows, st.Cursor, box.width); box.promptRow+row != s.Row || grokTextCol+col != s.Col {
		return Lock{}, false
	}
	p.remember(box, st)
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

// parse reads the text rows back and verifies the reading by laying it out
// again (same reading rules as Claude Code's wrap-ansi composer).
func (p *grokProfile) parse(s *Screen, box grokBox) (State, bool) {
	w := box.width
	n := box.lastRow - box.promptRow + 1
	texts := make([]string, n)
	widths := make([]int, n)
	for i := 0; i < n; i++ {
		texts[i], widths[i] = rowRun(s, box.promptRow+i, grokTextCol, grokTextCol+w)
		if grokChipRe.MatchString(texts[i]) {
			return State{}, false
		}
	}
	seps := make([][]string, n-1)
	ambiguous := 0
	for i := 0; i < n-1; i++ {
		first := texts[i+1]
		if j := strings.IndexByte(first, ' '); j >= 0 {
			first = first[:j]
		}
		switch {
		case widths[i] >= w:
			seps[i] = []string{"", " "}
			ambiguous++
		case widths[i] > 0 && first != "" && widths[i]+1+StringWidth(first) > w:
			seps[i] = []string{" "}
		default:
			seps[i] = []string{"\n"}
		}
	}
	if ambiguous > 4 {
		return State{}, false
	}
	cursorRow, cursorCol := s.Row-box.promptRow, s.Col-grokTextCol
	verify := func(text string) (State, bool) {
		if cursorRow == n-1 && cursorCol > widths[n-1] {
			text += strings.Repeat(" ", cursorCol-widths[n-1])
		}
		elems := TextElems(text)
		rows := grokWrap(elems, w)
		if len(rows) != n {
			return State{}, false
		}
		for i, got := range layoutStrings(elems, rows) {
			if got != texts[i] {
				return State{}, false
			}
		}
		for c := len(elems); c >= 0; c-- {
			if row, col := grokCursor(elems, rows, c, w); row == cursorRow && col == cursorCol {
				return State{Elems: elems, Cursor: c}, true
			}
		}
		return State{}, false
	}
	choice := make([]int, n-1)
	for {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(texts[i])
			if i < n-1 {
				b.WriteString(seps[i][choice[i]])
			}
		}
		if st, ok := verify(b.String()); ok {
			return st, true
		}
		i := 0
		for ; i < n-1; i++ {
			if choice[i]+1 < len(seps[i]) {
				choice[i]++
				break
			}
			choice[i] = 0
		}
		if i == n-1 {
			return State{}, false
		}
	}
}

func (p *grokProfile) Same(a, b State, s *Screen) bool {
	if a.Mode != b.Mode {
		return false
	}
	w := max(1, s.Cols-3-1-grokTextCol)
	ra, rb := grokWrap(a.Elems, w), grokWrap(b.Elems, w)
	if len(ra) != len(rb) {
		return false
	}
	la, lb := layoutStrings(a.Elems, ra), layoutStrings(b.Elems, rb)
	for i := range la {
		if la[i] != lb[i] {
			return false
		}
	}
	xa, ya := grokCursor(a.Elems, ra, a.Cursor, w)
	xb, yb := grokCursor(b.Elems, rb, b.Cursor, w)
	return xa == xb && ya == yb
}

func (p *grokProfile) Render(lock Lock, st State, s *Screen) (Paint, bool) {
	box, ok := lock.Box.(grokBox)
	if !ok || grokChipRe.MatchString(st.Text()) {
		return Paint{}, false
	}
	w := box.width
	rows := grokWrap(st.Elems, w)
	n := len(rows)
	if n > p.maxRows(s.Rows) {
		return Paint{}, false
	}
	newTop := box.lastRow - (n - 1)
	oldBorder, newBorder := box.promptRow-1, newTop-1
	if newBorder < 0 {
		return Paint{}, false
	}
	// Growth takes blank rows above the box; Grok's transcript above is not
	// modelled, so growth into it waits for the server.
	for r := newBorder; r < oldBorder; r++ {
		if !rowBlank(s, r, 0, s.Cols) {
			return Paint{}, false
		}
	}
	paint := Paint{Rows: map[int][]Cell{}}
	for i, vr := range rows {
		row := append([]Cell(nil), box.promptRowCells...)
		for c := 3; c < box.right; c++ {
			row[c] = Cell{Width: 1, Attr: box.blank}
		}
		if i == 0 {
			row[4] = box.promptRowCells[4]
		}
		rowCells(row, grokTextCol, st.Elems[vr.Start+vr.Hidden:vr.End], box.blank, box.blank)
		paint.Rows[newTop+i] = row
	}
	if newBorder != oldBorder {
		paint.Rows[newBorder] = append([]Cell(nil), box.topBorder...)
	}
	for r := oldBorder; r < newBorder; r++ {
		paint.Rows[r] = blankRow(s.Cols, box.screenBG)
	}
	// The footer depends on whether the draft is empty.
	if lock.State.Empty() != st.Empty() {
		footer := p.typingFooter
		if st.Empty() {
			footer = p.emptyFooter
		}
		if len(footer) == len(box.footer) {
			for i, row := range footer {
				if len(row) == s.Cols {
					paint.Rows[box.lastRow+2+i] = append([]Cell(nil), row...)
				}
			}
		}
	}
	crow, ccol := grokCursor(st.Elems, rows, st.Cursor, w)
	paint.CursorRow, paint.CursorCol = newTop+crow, grokTextCol+ccol
	return paint, true
}

func (p *grokProfile) Edit(k Key, st State, ctx EditContext) EditFunc {
	if p.width == 0 || slashCommand(st) {
		// A slash command shows the rest of its best match inline after the
		// cursor ("/he" draws "/help"); it is not predicted.
		return nil
	}
	switch k.Kind {
	case KeyText:
		if !printable(k.Text) {
			return nil
		}
		if st.Empty() && strings.ContainsAny(k.Text[:1], "/@!?#") {
			return nil // commands, mentions and shortcuts open popups
		}
		elems := TextElems(k.Text)
		return func(s State) State { return s.Insert(elems...) }
	case KeyNewline:
		return func(s State) State { return s.Insert(Elem{S: "\n"}) }
	case KeyBackspace:
		return func(s State) State { return s.DeleteBackward() }
	case KeyDelete:
		return func(s State) State { return s.DeleteForward() }
	case KeyLeft:
		return func(s State) State { return s.Move(-1) }
	case KeyRight:
		return func(s State) State { return s.Move(1) }
	case KeyPaste:
		text := normalizePaste(k.Text)
		// Four or more lines become a chip (a different layout); keep inline
		// pastes short.
		if text == "" || countNewlines(text) >= 3 || len(text) > 400 || looksLikeImagePath(text) || !printable(strings.ReplaceAll(text, "\n", " ")) {
			return nil
		}
		if st.Empty() && strings.ContainsAny(text[:1], "/@!?#") {
			return nil
		}
		elems := TextElems(text)
		return func(s State) State { return s.Insert(elems...) }
	}
	return grokEditing.edit(k, st, p.width)
}

// grokEditing: Home/End stay on the logical line; Ctrl+B and Ctrl+F do not
// move the cursor (recorded on Grok Build 1.0.77).
var grokEditing = editingKeys{wrap: grokWrap}

// grokCursor maps the cursor like cursorRowCol, with the hidden trailing
// space of a full row kept at the row's end.
func grokCursor(elems []Elem, rows []VisualRow, cursor, w int) (int, int) {
	row, col := cursorRowCol(elems, rows, cursor, w)
	return row, min(col, w)
}

// grokWrap is Grok's word wrap: words move to the next row when they do not
// fit, words longer than a row are broken, and spaces that do not fit at the
// end of a row start the next row hidden and take no width there (so a space
// typed after an exactly full row creates no row until the next character).
func grokWrap(elems []Elem, w int) []VisualRow {
	w = max(1, w)
	var rows []VisualRow
	for _, line := range logicalLines(elems) {
		rows = append(rows, grokWrapLine(elems, line[0], line[1], w)...)
	}
	return rows
}

func grokWrapLine(elems []Elem, start, end, w int) []VisualRow {
	var rows []VisualRow
	row := VisualRow{Start: start, End: start}
	soft := false // the current row began by soft wrap
	next := func(at int) {
		rows = append(rows, row)
		row, soft = VisualRow{Start: at, End: at}, true
	}
	isSpace := func(i int) bool { return !elems[i].Token && elems[i].S == " " }
	for i := start; i < end; {
		if isSpace(i) {
			switch {
			case soft && row.End == row.Start+row.Hidden:
				row.Hidden++ // leading space of a wrapped row
				row.End = i + 1
			case row.Width+1 <= w:
				row.Width++
				row.End = i + 1
			default:
				next(i)
				row.Hidden, row.End = 1, i+1
			}
			i++
			continue
		}
		j, word := i, 0
		for j < end && !isSpace(j) {
			word += elemWidth(elems[j])
			j++
		}
		if row.Width+word > w && row.Width > 0 {
			next(i)
		}
		for ; i < j; i++ {
			cw := elemWidth(elems[i])
			if row.Width+cw > w && row.Width > 0 {
				next(i)
			}
			row.Width += cw
			row.End = i + 1
		}
	}
	rows = append(rows, row)
	// A trailing row of hidden spaces does not exist yet.
	if n := len(rows); n > 1 && rows[n-1].Width == 0 && rows[n-1].Hidden > 0 && rows[n-1].End == end {
		rows[n-2].End = end
		rows = rows[:n-1]
	}
	return rows
}
