package predict

import (
	"fmt"
	"regexp"
	"strings"
)

// OpenCode's composer (OpenTUI, alternate screen), recorded at 100x30 in both
// layouts it uses:
//
//	┃                                   pad row
//	┃  text…                            text from bar+3, width = edge-bar-4
//	┃                                   pad row
//	┃  Build auto · model · provider    status row
//	╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀     bottom edge
//	path / usage                       footer row
//
// On the home screen (before the first message) the logo, the composer and
// the footer form one block centered vertically: growing by a row moves the
// block's top up or its bottom down, alternately. In a session the composer is
// anchored to the bottom and grows upward. Empty drafts show a gray
// placeholder. Pastes of three or more lines (or long ones) become a
// "[Pasted ~N lines] " chip, which Backspace deletes as a whole.
const (
	ocTextFG      = 0xeeeeee
	ocPlaceholder = 0x808080
)

var ocChipRe = regexp.MustCompile(`\[Pasted ~\d+ lines?\]`)

type ocBox struct {
	bar, edge          int // bar column, last column of the bottom edge
	width              int
	topPad, lastText   int // rows
	bottomEdge, footer int
	home               bool
	blockTop           int // home screen: first row of the centered block
	padRow             []Cell
	text               Attr // a typed character's attributes
	blank              Attr
	screenBG           Attr
}

type opencodeProfile struct {
	width       int
	placeholder []Cell // home/session placeholder from the text column
	text        Attr
	blockTop    int // home screen: the block top last seen with its logo
}

func newOpencodeProfile() *opencodeProfile {
	return &opencodeProfile{blockTop: -1}
}

func (p *opencodeProfile) Name() string { return "opencode" }

func (p *opencodeProfile) maxRows(rows int) int { return max(1, rows/3) }

func (p *opencodeProfile) geometry(s *Screen) (ocBox, bool) {
	fail := func() (ocBox, bool) { return ocBox{}, false }
	if !s.CursorVisible || s.Cols < 20 {
		return fail()
	}
	r := s.Row
	bar := -1
	for c := 0; c+3 <= s.Col; c++ {
		if s.Cell(r, c).Text == "┃" {
			bar = c
			break
		}
	}
	if bar < 0 || s.Col < bar+3 || !s.Cell(r, bar+1).Blank() || !s.Cell(r, bar+2).Blank() {
		return fail()
	}
	box := ocBox{bar: bar, home: bar > 2}
	bottom := r + 1
	for bottom < s.Rows && s.Cell(bottom, bar).Text == "┃" {
		bottom++
	}
	if bottom >= s.Rows || s.Cell(bottom, bar).Text != "╹" || bottom-3 < r {
		return fail()
	}
	box.bottomEdge, box.footer = bottom, bottom+1
	box.lastText = bottom - 3
	box.edge = bar
	for c := bar + 1; c < s.Cols && s.Cell(bottom, c).Text == "▀"; c++ {
		box.edge = c
	}
	box.width = box.edge - bar - 4
	if box.width < 8 || !rowBlank(s, bottom-2, bar+1, box.edge+1) {
		return fail()
	}
	// A command or mention popup sits on the rows above the box with the same
	// left bar; its rows also close with a bar on the right.
	popupRow := func(row int) bool {
		for c := bar + 1; c < s.Cols; c++ {
			if s.Cell(row, c).Text == "┃" {
				return true
			}
		}
		return false
	}
	top := r
	for top-1 >= 0 && s.Cell(top-1, bar).Text == "┃" && !popupRow(top-1) {
		top--
	}
	box.topPad = top
	if top >= r || !rowBlank(s, top, bar+1, box.edge+1) || box.lastText-box.topPad > p.maxRows(s.Rows) {
		return fail()
	}
	// Nothing right of the text area inside the box.
	for row := top; row <= box.lastText; row++ {
		if !rowBlank(s, row, bar+3+box.width+1, box.edge+1) {
			return fail()
		}
	}
	box.padRow = copyRow(s, top)
	box.blank = s.Cell(top, bar+1).Attr
	box.screenBG = Attr{BG: s.Cell(top, 0).Attr.BG}
	box.text = Attr{FG: Color{ColorRGB, ocTextFG}, BG: box.blank.BG}
	if box.home {
		// The block is centered: its top satisfies top = (rows+1-height)/2.
		// Integer division admits two tops; the real one is where the logo
		// starts (content on that row, blank above it).
		box.blockTop = -1
		for t := 0; t <= box.topPad; t++ {
			if t == (s.Rows+1-(box.footer-t+1))/2 && !rowBlank(s, t, 0, s.Cols) && (t == 0 || rowBlank(s, t-1, 0, s.Cols)) {
				box.blockTop = t
				break
			}
		}
		// A popup may cover the logo: keep the top found before if it still
		// centers this block.
		if t := p.blockTop; box.blockTop < 0 && t >= 0 && t <= box.topPad && t == (s.Rows+1-(box.footer-t+1))/2 {
			box.blockTop = t
		}
		if box.blockTop < 0 {
			return fail()
		}
		p.blockTop = box.blockTop
	}
	p.width = box.width
	return box, true
}

// placeholderRow: the single text row shows only gray text, at the cursor.
func ocPlaceholderShown(s *Screen, box ocBox) bool {
	if box.lastText != box.topPad+1 || s.Row != box.lastText || s.Col != box.bar+3 {
		return false
	}
	seen := false
	for c := box.bar + 3; c <= box.edge; c++ {
		cell := s.Cell(box.lastText, c)
		if cell.Blank() {
			continue
		}
		if cell.Attr.FG != (Color{ColorRGB, ocPlaceholder}) {
			return false
		}
		seen = true
	}
	return seen
}

func (p *opencodeProfile) Locate(s *Screen) (Lock, bool) {
	box, ok := p.geometry(s)
	if !ok {
		return Lock{}, false
	}
	if ocPlaceholderShown(s, box) {
		p.placeholder = append([]Cell(nil), s.grid[box.lastText][box.bar+3:]...)
		return Lock{State: State{}, Top: box.topPad + 1, Bottom: box.lastText, Box: box}, true
	}
	st, ok := p.parse(s, box)
	if !ok {
		return Lock{}, false
	}
	if st.Empty() {
		p.placeholder = append([]Cell(nil), s.grid[box.lastText][box.bar+3:]...)
	}
	return Lock{State: st, Top: box.topPad + 1, Bottom: box.lastText, Box: box}, true
}

func (p *opencodeProfile) Match(s *Screen, st State) (Lock, bool) {
	box, ok := p.geometry(s)
	if !ok || st.Empty() || ocPlaceholderShown(s, box) {
		return Lock{}, false
	}
	rows := grokWrap(st.Elems, box.width)
	if len(rows) != box.lastText-box.topPad {
		return Lock{}, false
	}
	for i, text := range layoutStrings(st.Elems, rows) {
		if got, _ := rowRun(s, box.topPad+1+i, box.bar+3, box.bar+3+box.width); got != text {
			return Lock{}, false
		}
	}
	if row, col := grokCursor(st.Elems, rows, st.Cursor, box.width); box.topPad+1+row != s.Row || box.bar+3+col != s.Col {
		return Lock{}, false
	}
	return Lock{State: st, Top: box.topPad + 1, Bottom: box.lastText, Box: box}, true
}

func (p *opencodeProfile) parse(s *Screen, box ocBox) (State, bool) {
	w := box.width
	first := box.topPad + 1
	n := box.lastText - first + 1
	texts := make([]string, n)
	widths := make([]int, n)
	for i := 0; i < n; i++ {
		texts[i], widths[i] = rowRun(s, first+i, box.bar+3, box.bar+3+w)
	}
	seps := make([][]string, n-1)
	ambiguous := 0
	for i := 0; i < n-1; i++ {
		word := texts[i+1]
		if j := strings.IndexByte(word, ' '); j >= 0 {
			word = word[:j]
		}
		switch {
		case widths[i] >= w:
			seps[i] = []string{"", " "}
			ambiguous++
		case widths[i] > 0 && word != "" && widths[i]+1+StringWidth(word) > w:
			seps[i] = []string{" "}
		default:
			seps[i] = []string{"\n"}
		}
	}
	if ambiguous > 4 {
		return State{}, false
	}
	cursorRow, cursorCol := s.Row-first, s.Col-box.bar-3
	verify := func(text string) (State, bool) {
		if cursorRow == n-1 && cursorCol > widths[n-1] {
			text += strings.Repeat(" ", cursorCol-widths[n-1])
		}
		elems := tokenize(text, ocChipRe)
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

func (p *opencodeProfile) Same(a, b State, s *Screen) bool {
	if a.Mode != b.Mode || a.Empty() != b.Empty() {
		return false
	}
	w := p.width
	if w == 0 {
		return a.Equal(b)
	}
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

func (p *opencodeProfile) Render(lock Lock, st State, s *Screen) (Paint, bool) {
	box, ok := lock.Box.(ocBox)
	if !ok {
		return Paint{}, false
	}
	w := box.width
	rows := grokWrap(st.Elems, w)
	n := len(rows)
	if n > p.maxRows(s.Rows) {
		return Paint{}, false
	}
	n0 := box.lastText - box.topPad
	delta := n - n0
	paint := Paint{Rows: map[int][]Cell{}}
	textRow := func(i int, vr VisualRow) []Cell {
		row := append([]Cell(nil), box.padRow...)
		if st.Empty() && i == 0 && p.placeholder != nil {
			copy(row[box.bar+3:], p.placeholder)
			return row
		}
		rowCells(row, box.bar+3, st.Elems[vr.Start+vr.Hidden:vr.End], box.text, box.text)
		return row
	}
	var newTopPad int
	if box.home {
		// Centered block: rows above the box move with the block's top,
		// rows from the bottom pad down move with its bottom.
		h0 := box.footer - box.blockTop + 1
		h := h0 + delta
		top := (s.Rows + 1 - h) / 2
		if top < 0 || top+h > s.Rows {
			return Paint{}, false
		}
		dTop, dBot := top-box.blockTop, (top+h-1)-box.footer
		lo, hi := min(box.blockTop, top), max(box.footer, top+h-1)
		for r := lo; r <= hi; r++ {
			paint.Rows[r] = blankRow(s.Cols, box.screenBG)
		}
		if delta != 0 {
			for r := box.blockTop; r < box.topPad; r++ {
				paint.Rows[r+dTop] = copyRow(s, r)
			}
			for r := box.lastText + 1; r <= box.footer; r++ {
				paint.Rows[r+dBot] = copyRow(s, r)
			}
		} else {
			for r := lo; r <= hi; r++ {
				if r < box.topPad || r > box.lastText {
					paint.Rows[r] = copyRow(s, r)
				}
			}
		}
		newTopPad = box.topPad + dTop
	} else {
		// Bottom-anchored: grow upward into blank rows above the box.
		newTopPad = box.topPad - delta
		if newTopPad < 0 {
			return Paint{}, false
		}
		for r := newTopPad; r < box.topPad; r++ {
			if !rowBlank(s, r, 0, s.Cols) {
				return Paint{}, false
			}
		}
		for r := box.topPad; r < newTopPad; r++ {
			paint.Rows[r] = blankRow(s.Cols, box.screenBG)
		}
	}
	paint.Rows[newTopPad] = append([]Cell(nil), box.padRow...)
	for i, vr := range rows {
		paint.Rows[newTopPad+1+i] = textRow(i, vr)
	}
	crow, ccol := grokCursor(st.Elems, rows, st.Cursor, w)
	if st.Empty() {
		crow, ccol = 0, 0
	}
	paint.CursorRow, paint.CursorCol = newTopPad+1+crow, box.bar+3+ccol
	return paint, true
}

func (p *opencodeProfile) Edit(k Key, st State, ctx EditContext) EditFunc {
	if p.width == 0 {
		return nil
	}
	switch k.Kind {
	case KeyText:
		if !printable(k.Text) {
			return nil
		}
		if st.Empty() && strings.HasPrefix(k.Text, "!") {
			return nil // shell mode
		}
		// "/" and "@" open a popup over the rows above the box; the box
		// itself just shows the text.
		elems := TextElems(k.Text)
		return func(s State) State { return s.Insert(elems...) }
	case KeyTab:
		if menuOpen(st) {
			return nil // completes the selected entry
		}
		return func(s State) State { return s } // no effect on the draft
	case KeyUp, KeyDown:
		if menuOpen(st) {
			return nil // moves the popup selection
		}
		return opencodeEditing.edit(k, st, p.width)
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
		return opencodePaste(normalizePaste(k.Text), st)
	}
	return opencodeEditing.edit(k, st, p.width)
}

// opencodePaste predicts a bracketed paste (recorded on OpenCode 2.3.0): three
// or more lines, or more than 150 characters, become a "[Pasted ~N lines]"
// chip followed by a space, where N counts the lines without a trailing line
// break; shorter pastes are inserted as text. Pastes where a trailing break
// decides between the two (or several trailing or leading breaks) wait.
func opencodePaste(text string, st State) EditFunc {
	if text == "" || looksLikeImagePath(text) || !printable(strings.ReplaceAll(text, "\n", " ")) {
		return nil
	}
	if st.Empty() && strings.ContainsAny(text[:1], "/@!") {
		return nil
	}
	body := strings.TrimSuffix(text, "\n")
	if strings.HasPrefix(text, "\n") || strings.HasSuffix(body, "\n") {
		return nil
	}
	lines := strings.Count(body, "\n") + 1
	long := len(body) > 150
	if body != text && (lines == 2 || long != (len(text) > 150)) {
		return nil
	}
	if lines >= 3 || long {
		chip := Elem{S: fmt.Sprintf("[Pasted ~%d lines]", lines), Token: true}
		return func(s State) State { return s.Insert(chip, Elem{S: " "}) }
	}
	elems := TextElems(text)
	return func(s State) State { return s.Insert(elems...) }
}

// opencodeEditing: Home/End go to the start/end of the whole draft (recorded
// on OpenCode 2.3.0).
var opencodeEditing = editingKeys{homeEndBuffer: true, ctrlBF: true, wrap: grokWrap}
