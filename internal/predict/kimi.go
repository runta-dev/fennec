package predict

// Kimi Code 2.6.2 bundles pi-tui's editor in a rounded box and always draws
// its own cursor (a reverse-video cell; there is no hardware-cursor option,
// and the hidden terminal cursor is not reliably at the insertion point).
// Recorded at 100x30:
//
//	╭────────────────────────────╮    top border, column 1
//	│ > text from column 5       │    ">" at column 3 on the first row
//	│   continuation             │
//	╰────────────────────────────╯
//	footer rows (mode, model, hints, context)
//
// Text wraps with piWrap at columns-10. The transcript is top-aligned and the
// box grows downward, moving the footer into the blank rows below it.
type kimiProfile struct {
	piProfile
}

type kimiBox struct {
	promptRow, lastRow int
	footerEnd          int      // last non-blank row below the box
	prompt, cont       []Cell   // row templates: first and continuation rows
	bottom             []Cell   // bottom border
	footer             [][]Cell // rows below the bottom border, to footerEnd
	text               Attr
}

const kimiTextCol = 5

func newKimiProfile() *kimiProfile { return &kimiProfile{piProfile{name: "kimi"}} }

func (p *kimiProfile) textWidth(cols int) int { return cols - 10 }

func (p *kimiProfile) geometry(s *Screen) (kimiBox, int, int, bool) {
	if s.Cols < 30 {
		return kimiBox{}, 0, 0, false
	}
	for r := 1; r < s.Rows-1; r++ {
		if s.Cell(r, 1).Text != "│" || s.Cell(r, 3).Text != ">" || s.Cell(r-1, 1).Text != "╭" {
			continue
		}
		box := kimiBox{promptRow: r, lastRow: r}
		for box.lastRow+1 < s.Rows && s.Cell(box.lastRow+1, 1).Text == "│" {
			box.lastRow++
			if !s.Cell(box.lastRow, 3).Blank() {
				return kimiBox{}, 0, 0, false
			}
		}
		bottom := box.lastRow + 1
		if bottom >= s.Rows || s.Cell(bottom, 1).Text != "╰" || box.lastRow-box.promptRow+1 > p.maxRows(s.Rows) {
			return kimiBox{}, 0, 0, false
		}
		w := p.textWidth(s.Cols)
		// The drawn cursor: exactly one reverse-video cell in the text area.
		crow, ccol, found := 0, 0, 0
		for row := box.promptRow; row <= box.lastRow; row++ {
			for c := kimiTextCol; c <= kimiTextCol+w; c++ {
				if s.Cell(row, c).Attr.Flags&AttrInverse != 0 {
					crow, ccol = row, c
					found++
				}
			}
			// Nothing between the text area and the right border.
			if !rowBlank(s, row, kimiTextCol+w+1, s.Cols-2) {
				return kimiBox{}, 0, 0, false
			}
		}
		if found != 1 {
			return kimiBox{}, 0, 0, false
		}
		box.prompt = copyRow(s, box.promptRow)
		if box.lastRow > box.promptRow {
			box.cont = copyRow(s, box.promptRow+1)
		} else {
			box.cont = copyRow(s, box.promptRow)
			box.cont[3] = Cell{Text: "", Width: 1}
		}
		box.bottom = copyRow(s, bottom)
		box.footerEnd = bottom
		for row := bottom + 1; row < s.Rows; row++ {
			if !rowBlank(s, row, 0, s.Cols) {
				box.footerEnd = row
			}
		}
		for row := bottom + 1; row <= box.footerEnd; row++ {
			box.footer = append(box.footer, copyRow(s, row))
		}
		p.width = w
		return box, crow, ccol, true
	}
	return kimiBox{}, 0, 0, false
}

func (p *kimiProfile) Locate(s *Screen) (Lock, bool) {
	box, crow, ccol, ok := p.geometry(s)
	if !ok {
		return Lock{}, false
	}
	st, ok := p.parse(s, box, crow, ccol)
	if !ok {
		return Lock{}, false
	}
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

func (p *kimiProfile) Match(s *Screen, st State) (Lock, bool) {
	box, crow, ccol, ok := p.geometry(s)
	if !ok || st.Empty() || !p.shows(s, box, st, crow, ccol) {
		return Lock{}, false
	}
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

func (p *kimiProfile) shows(s *Screen, box kimiBox, st State, crow, ccol int) bool {
	w := p.textWidth(s.Cols)
	rows := piWrap(st.Elems, w)
	if len(rows) != box.lastRow-box.promptRow+1 || !piFits(st.Elems, w) {
		return false
	}
	for i, text := range layoutStrings(st.Elems, rows) {
		if got, _ := rowRun(s, box.promptRow+i, kimiTextCol, kimiTextCol+w); got != text {
			return false
		}
	}
	row, col := piCursor(st.Elems, rows, st.Cursor)
	return box.promptRow+row == crow && kimiTextCol+col == ccol
}

func (p *kimiProfile) parse(s *Screen, box kimiBox, crow, ccol int) (State, bool) {
	w := p.textWidth(s.Cols)
	n := box.lastRow - box.promptRow + 1
	texts := make([]string, n)
	for i := range texts {
		texts[i], _ = rowRun(s, box.promptRow+i, kimiTextCol, kimiTextCol+w)
	}
	seps := make([][]string, n-1)
	for i := range seps {
		last, next := []rune(texts[i]), []rune(texts[i+1])
		switch {
		case len(last) == 0 || len(next) == 0:
			seps[i] = []string{"\n", " "}
		case piCJK(Elem{S: string(last[len(last)-1])}) || piCJK(Elem{S: string(next[0])}):
			seps[i] = []string{"", " ", "\n"}
		default:
			seps[i] = []string{" ", "", "\n"}
		}
	}
	verify := func(text string) (State, bool) {
		elems := TextElems(text)
		if crow == box.lastRow {
			if _, width := rowRun(s, crow, kimiTextCol, kimiTextCol+w); ccol-kimiTextCol > width {
				elems = append(elems, TextElems(spaces(ccol-kimiTextCol-width))...)
			}
		}
		if !piFits(elems, w) {
			return State{}, false
		}
		rows := piWrap(elems, w)
		if !piLayout(elems, rows, texts) {
			return State{}, false
		}
		if c, ok := piFind(elems, rows, crow-box.promptRow, ccol-kimiTextCol); ok {
			return State{Elems: elems, Cursor: c}, true
		}
		return State{}, false
	}
	choice := make([]int, n-1)
	for tries := 0; tries < 2000; tries++ {
		text := ""
		for i := 0; i < n; i++ {
			text += texts[i]
			if i < n-1 {
				text += seps[i][choice[i]]
			}
		}
		if st, ok := verify(text); ok {
			return st, true
		}
		i := n - 2
		for ; i >= 0; i-- {
			if choice[i]+1 < len(seps[i]) {
				choice[i]++
				break
			}
			choice[i] = 0
		}
		if i < 0 {
			break
		}
	}
	return State{}, false
}

func spaces(n int) string {
	b := make([]byte, max(0, n))
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

func (p *kimiProfile) Render(lock Lock, st State, s *Screen) (Paint, bool) {
	box, ok := lock.Box.(kimiBox)
	if !ok {
		return Paint{}, false
	}
	w := p.textWidth(s.Cols)
	rows := piWrap(st.Elems, w)
	n := len(rows)
	if n > p.maxRows(s.Rows) || !piFits(st.Elems, w) {
		return Paint{}, false
	}
	delta := n - (box.lastRow - box.promptRow + 1)
	// The footer moves with the bottom border; it needs blank rows below.
	if box.footerEnd+delta >= s.Rows {
		return Paint{}, false
	}
	for r := box.footerEnd + 1; r <= box.footerEnd+delta; r++ {
		if !rowBlank(s, r, 0, s.Cols) {
			return Paint{}, false
		}
	}
	paint := Paint{Rows: map[int][]Cell{}, DrawnCursor: true}
	crow, ccol := piCursor(st.Elems, rows, st.Cursor)
	for i, vr := range rows {
		row := append([]Cell(nil), box.cont...)
		if i == 0 {
			row = append([]Cell(nil), box.prompt...)
		}
		for c := kimiTextCol; c <= kimiTextCol+w; c++ {
			row[c] = Cell{Text: "", Width: 1}
		}
		rowCells(row, kimiTextCol, st.Elems[vr.Start:vr.End], box.text, box.text)
		if i == crow {
			cell := &row[kimiTextCol+ccol]
			if cell.Width == 0 && kimiTextCol+ccol > 0 {
				return Paint{}, false
			}
			cell.Attr.Flags |= AttrInverse
		}
		paint.Rows[box.promptRow+i] = row
	}
	bottom := box.promptRow + n
	if delta != 0 {
		paint.Rows[bottom] = append([]Cell(nil), box.bottom...)
		for i, f := range box.footer {
			paint.Rows[bottom+1+i] = append([]Cell(nil), f...)
		}
		for r := bottom + 1 + len(box.footer); r <= box.footerEnd; r++ {
			paint.Rows[r] = blankRow(s.Cols, Attr{})
		}
	}
	paint.CursorRow, paint.CursorCol = box.promptRow+crow, kimiTextCol+ccol
	return paint, true
}
