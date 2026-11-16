package predict

import (
	"strings"
)

// pi-tui's editor, used by Pi (launched with PI_HARDWARE_CURSOR=1, so the
// terminal cursor marks the insertion point) and Kimi Code, recorded at
// 100x30 on Pi 1.5.2:
//
//	────────────────────────────   top border: the full width, "─"
//	text from column 0, wrapped    the last column stays free for the cursor
//	────────────────────────────   bottom border
//	footer rows (cwd, status)      never move
//
// The box is anchored at the bottom and grows upward into the blank rows
// below the top-aligned transcript; at most max(5, rows*30%) text rows show
// before it scrolls inside (not predicted). Text wraps with piWrap. Large
// pastes become "[paste #N ...]" markers (not predicted).
type piProfile struct {
	name  string
	width int
}

type piBox struct {
	top, bottom int // border rows
	border      []Cell
	text        Attr
}

func newPiProfile() *piProfile { return &piProfile{name: "pi"} }

func (p *piProfile) Name() string { return p.name }

func (p *piProfile) maxRows(rows int) int { return max(5, rows*3/10) }

func piBorderRow(s *Screen, r int) bool {
	if r < 0 || r >= s.Rows {
		return false
	}
	for c := 0; c < s.Cols; c++ {
		if s.Cell(r, c).Text != "─" {
			return false
		}
	}
	return true
}

func (p *piProfile) geometry(s *Screen) (piBox, bool) {
	if !s.CursorVisible || s.Cols < 20 {
		return piBox{}, false
	}
	top := s.Row - 1
	for top >= 0 && !piBorderRow(s, top) {
		top--
	}
	bottom := s.Row + 1
	for bottom < s.Rows && !piBorderRow(s, bottom) {
		bottom++
	}
	n := bottom - top - 1
	if top < 0 || bottom >= s.Rows || n < 1 || n > p.maxRows(s.Rows) {
		return piBox{}, false
	}
	for r := top + 1; r < bottom; r++ {
		if piBorderRow(s, r) || !s.Cell(r, s.Cols-1).Blank() {
			return piBox{}, false
		}
	}
	box := piBox{top: top, bottom: bottom, border: copyRow(s, top)}
	for r := top + 1; r < bottom && box.text == (Attr{}); r++ {
		for c := 0; c < s.Cols-1; c++ {
			if cell := s.Cell(r, c); !cell.Blank() {
				box.text = cell.Attr
				break
			}
		}
	}
	p.width = s.Cols - 1
	return box, true
}

func (p *piProfile) Locate(s *Screen) (Lock, bool) {
	box, ok := p.geometry(s)
	if !ok {
		return Lock{}, false
	}
	st, ok := p.parse(s, box)
	if !ok {
		return Lock{}, false
	}
	return Lock{State: st, Top: box.top + 1, Bottom: box.bottom - 1, Box: box}, true
}

func (p *piProfile) Match(s *Screen, st State) (Lock, bool) {
	box, ok := p.geometry(s)
	if !ok || st.Empty() {
		return Lock{}, false
	}
	if p.shows(s, box, st) {
		return Lock{State: st, Top: box.top + 1, Bottom: box.bottom - 1, Box: box}, true
	}
	return Lock{}, false
}

// shows reports whether the box displays st with the cursor where st has it.
func (p *piProfile) shows(s *Screen, box piBox, st State) bool {
	w := s.Cols - 1
	rows := piWrap(st.Elems, w)
	if len(rows) != box.bottom-box.top-1 || !piFits(st.Elems, w) {
		return false
	}
	for i, text := range layoutStrings(st.Elems, rows) {
		if got, _ := rowRun(s, box.top+1+i, 0, w); got != text {
			return false
		}
	}
	row, col := piCursor(st.Elems, rows, st.Cursor)
	return box.top+1+row == s.Row && col == s.Col
}

// piCursor places the cursor like pi-tui's layoutText: inside a row, or at
// the end of the last row of its logical line.
func piCursor(elems []Elem, rows []VisualRow, cursor int) (int, int) {
	for i, r := range rows {
		lastOfLine := i == len(rows)-1 || rows[i+1].Start > r.End
		if cursor >= r.Start && (cursor < r.End || cursor == r.End && lastOfLine) {
			col := 0
			for _, e := range elems[r.Start:cursor] {
				col += elemWidth(e)
			}
			return i, col
		}
	}
	return len(rows) - 1, 0
}

// piLayout: rows laid out from elems show texts.
func piLayout(elems []Elem, rows []VisualRow, texts []string) bool {
	if len(rows) != len(texts) {
		return false
	}
	for i, got := range layoutStrings(elems, rows) {
		if got != texts[i] {
			return false
		}
	}
	return true
}

// piFind returns the last element index whose cursor lands at row, col.
func piFind(elems []Elem, rows []VisualRow, row, col int) (int, bool) {
	if row < 0 || row >= len(rows) {
		return 0, false
	}
	for c := min(len(elems), rows[row].End+1); c >= rows[row].Start; c-- {
		if r, cc := piCursor(elems, rows, c); r == row && cc == col {
			return c, true
		}
	}
	return 0, false
}

// parse reads the text rows back. A row boundary is a soft wrap (the
// row kept its trailing space, which the screen does not show, or a word
// or CJK text broke there) or a typed line break; readings are tried and
// the layout and cursor decide.
func (p *piProfile) parse(s *Screen, box piBox) (State, bool) {
	w := s.Cols - 1
	n := box.bottom - box.top - 1
	texts := make([]string, n)
	for i := range texts {
		texts[i], _ = rowRun(s, box.top+1+i, 0, w)
		if strings.Contains(texts[i], "[paste #") {
			return State{}, false
		}
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
	cursorRow, cursorCol := s.Row-box.top-1, s.Col
	verify := func(text string) (State, bool) {
		elems := TextElems(text)
		// Spaces typed at the very end show only through the cursor.
		if cursorRow == n-1 {
			if _, width := rowRun(s, s.Row, 0, w); cursorCol > width {
				elems = append(elems, TextElems(strings.Repeat(" ", cursorCol-width))...)
			}
		}
		if !piFits(elems, w) {
			return State{}, false
		}
		rows := piWrap(elems, w)
		if !piLayout(elems, rows, texts) {
			return State{}, false
		}
		if c, ok := piFind(elems, rows, cursorRow, cursorCol); ok {
			return State{Elems: elems, Cursor: c}, true
		}
		return State{}, false
	}
	choice := make([]int, n-1)
	for tries := 0; tries < 2000; tries++ {
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

func (p *piProfile) Same(a, b State, s *Screen) bool {
	if a.Equal(b) {
		return true
	}
	w := max(1, p.width)
	ra, rb := piWrap(a.Elems, w), piWrap(b.Elems, w)
	la, lb := layoutStrings(a.Elems, ra), layoutStrings(b.Elems, rb)
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if la[i] != lb[i] {
			return false
		}
	}
	xa, ya := piCursor(a.Elems, ra, a.Cursor)
	xb, yb := piCursor(b.Elems, rb, b.Cursor)
	return xa == xb && ya == yb
}

func (p *piProfile) Render(lock Lock, st State, s *Screen) (Paint, bool) {
	box, ok := lock.Box.(piBox)
	if !ok {
		return Paint{}, false
	}
	w := s.Cols - 1
	rows := piWrap(st.Elems, w)
	n := len(rows)
	if n > p.maxRows(s.Rows) || !piFits(st.Elems, w) {
		return Paint{}, false
	}
	newTop := box.bottom - n - 1
	if newTop < 0 {
		return Paint{}, false
	}
	paint := Paint{Rows: map[int][]Cell{}}
	switch {
	case newTop < box.top:
		// Growth takes blank rows above the box.
		for r := newTop; r < box.top; r++ {
			if !rowBlank(s, r, 0, s.Cols) {
				return Paint{}, false
			}
		}
	case newTop > box.top:
		// Shrinking leaves blank rows, known only below a blank gap.
		if box.top > 0 && !rowBlank(s, box.top-1, 0, s.Cols) {
			return Paint{}, false
		}
		for r := box.top; r < newTop; r++ {
			paint.Rows[r] = blankRow(s.Cols, Attr{})
		}
	}
	paint.Rows[newTop] = append([]Cell(nil), box.border...)
	for i, vr := range rows {
		row := blankRow(s.Cols, Attr{})
		rowCells(row, 0, st.Elems[vr.Start:vr.End], box.text, box.text)
		paint.Rows[newTop+1+i] = row
	}
	crow, ccol := piCursor(st.Elems, rows, st.Cursor)
	paint.CursorRow, paint.CursorCol = newTop+1+crow, ccol
	return paint, true
}

func (p *piProfile) Edit(k Key, st State, ctx EditContext) EditFunc {
	if p.width == 0 || slashCommand(st) {
		return nil
	}
	switch k.Kind {
	case KeyText:
		if !printable(k.Text) || strings.ContainsAny(k.Text, "\t") {
			return nil
		}
		if st.Empty() && strings.ContainsAny(k.Text[:1], "/!") {
			return nil // commands and bash mode
		}
		// "@" and "#" open completion popups below the box.
		if strings.ContainsAny(k.Text, "@#") || menuOpen(st) || piHashWord(st) {
			return nil
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
		lines := strings.Count(text, "\n") + 1
		// Larger pastes become a numbered marker; a path pasted after a word
		// gets a space; tabs are expanded.
		if text == "" || lines > 10 || len([]rune(text)) > 900 || strings.ContainsAny(text, "\t@#") ||
			strings.ContainsAny(text[:1], "/~.") || !printable(strings.ReplaceAll(text, "\n", " ")) {
			return nil
		}
		if st.Empty() && strings.HasPrefix(text, "!") {
			return nil
		}
		elems := TextElems(text)
		return func(s State) State { return s.Insert(elems...) }
	}
	return piEditing.edit(k, st, p.width)
}

// piEditing: Home/End and Ctrl+A/E stay on the logical line, Ctrl+B/F move
// by a character (recorded on Pi 1.5.2).
var piEditing = editingKeys{ctrlBF: true, wrap: piWrap}

// piHashWord: the word before the cursor starts with "#" (a completion popup).
func piHashWord(st State) bool {
	i := st.Cursor
	for i > 0 && !isSpaceElem(st.Elems[i-1]) {
		i--
	}
	return i < st.Cursor && st.Elems[i].S == "#"
}
