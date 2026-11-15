package predict

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Codex's composer (ratatui, alternate screen with an owned transcript):
//
//	pad row        composer background, blank
//	› text…        › bold at column 0, text from column 2
//	  continuation
//	pad row
//	status / footer rows (bottom of the screen)
//
// Text wraps at columns-3 with hanging interior spaces; a line exactly as wide
// as the wrap width gets an extra empty row. The block is bottom-anchored:
// growth shrinks the transcript above, which keeps showing its newest lines.
const (
	codexTextCol    = 2
	codexMaxRows    = 8
	codexLargePaste = 1000
)

var (
	codexTokenRe   = regexp.MustCompile(`\[(?:Pasted Content \d+ chars(?: #\d+)?|Image #\d+)\]`)
	codexImageRe   = regexp.MustCompile(`^\[Image #(\d+)\]$`)
	codexSeparator = "`~!@#$%^&*()-=+[{]}\\|;:'\",.<>/?"
)

type codexBox struct {
	prompt      [2]Cell
	padTop      []Cell
	padBottom   []Cell
	footer      [][]Cell
	bg          Attr
	width       int
	promptRow   int
	lastRow     int
	placeholder []Cell
}

type codexProfile struct {
	placeholder []Cell
	hints       []Cell // the footer's last row as shown with an empty draft
}

func newCodexProfile() *codexProfile { return &codexProfile{} }

func (p *codexProfile) Name() string { return "codex" }

func (p *codexProfile) textWidth(cols int) int { return max(1, cols-3) }

func codexPromptMode(s *Screen, r int) (string, bool) {
	c := s.Cell(r, 0)
	if !s.Cell(r, 1).Blank() {
		return "", false
	}
	switch c.Text {
	case "›":
		if c.Attr.Flags&AttrDim != 0 {
			return "", false // input disabled
		}
		return "", true
	case "!":
		return "shell", c.Attr.Flags&AttrBold != 0
	}
	return "", false
}

// geometry finds the composer block around the cursor.
func (p *codexProfile) geometry(s *Screen) (codexBox, string, bool) {
	fail := func() (codexBox, string, bool) { return codexBox{}, "", false }
	if !s.CursorVisible || s.Cols < 12 {
		return fail()
	}
	r := s.Row
	for r >= 0 {
		if _, ok := codexPromptMode(s, r); ok {
			break
		}
		if !s.Cell(r, 0).Blank() || !s.Cell(r, 1).Blank() {
			return fail()
		}
		r--
	}
	if r < 1 {
		return fail()
	}
	mode, _ := codexPromptMode(s, r)
	bg := Attr{BG: s.Cell(r, 1).Attr.BG}
	box := codexBox{promptRow: r, width: p.textWidth(s.Cols), bg: bg}
	// The pad row above must be blank: a thumbnail strip or scrolled view is
	// not modelled.
	if !rowBlank(s, r-1, 0, s.Cols) {
		return fail()
	}
	tinted := bg.BG.Kind != ColorDefault
	if tinted && s.Cell(r-1, 0).Attr.BG != bg.BG {
		return fail()
	}
	// Text rows end at the bottom pad row: the last tinted row, or without a
	// tint the first blank row.
	last := r
	for row := r + 1; row < s.Rows; row++ {
		if tinted {
			if s.Cell(row, 0).Attr.BG != bg.BG {
				break
			}
			last = row
			continue
		}
		if rowBlank(s, row, 0, s.Cols) {
			last = row
			break
		}
		if !s.Cell(row, 0).Blank() {
			return fail()
		}
		last = row
	}
	box.lastRow = last - 1 // the bottom pad row is not text
	if box.lastRow < r || !rowBlank(s, last, 0, s.Cols) || box.lastRow-r+1 > codexMaxRows {
		return fail()
	}
	if s.Row < box.promptRow || s.Row > box.lastRow || s.Col < codexTextCol {
		return fail()
	}
	// Scroll markers in the right column mean the view is partial.
	for row := box.promptRow; row <= box.lastRow; row++ {
		if t := s.Cell(row, s.Cols-1).Text; t == "↑" || t == "↓" {
			return fail()
		}
	}
	box.prompt = [2]Cell{s.Cell(r, 0), s.Cell(r, 1)}
	box.padTop = copyRow(s, r-1)
	box.padBottom = copyRow(s, last)
	// Everything below the block is footer (status line, then the
	// "? for shortcuts" hints while the draft is empty). The block is
	// anchored to it, so the footer's height moves the block.
	for row := last + 1; row < s.Rows; row++ {
		box.footer = append(box.footer, copyRow(s, row))
	}
	if len(box.footer) == 0 || len(box.footer) > 4 {
		return fail()
	}
	return box, mode, true
}

// footerFor returns the footer rows Codex shows for st, given the footer on
// screen for the locked state: the hints row exists only while the draft is
// empty.
func (p *codexProfile) footerFor(lock Lock, box codexBox, st State) ([][]Cell, bool) {
	if lock.State.Empty() == st.Empty() {
		return box.footer, true
	}
	if !st.Empty() {
		if len(box.footer) < 2 {
			return box.footer, true
		}
		return box.footer[:len(box.footer)-1], true
	}
	if p.hints == nil {
		return nil, false
	}
	return append(append([][]Cell(nil), box.footer...), p.hints), true
}

func (p *codexProfile) Locate(s *Screen) (Lock, bool) {
	box, mode, ok := p.geometry(s)
	if !ok {
		return Lock{}, false
	}
	r := box.promptRow
	// The dim placeholder means an empty draft.
	if cell := s.Cell(r, codexTextCol); !cell.Blank() && cell.Attr.Flags&AttrDim != 0 {
		if box.lastRow != r || s.Row != r || s.Col != codexTextCol || mode != "" {
			return Lock{}, false
		}
		box.placeholder = append([]Cell(nil), s.grid[r][codexTextCol:]...)
		p.placeholder = box.placeholder
		if len(box.footer) >= 2 {
			p.hints = box.footer[len(box.footer)-1]
		}
		return Lock{State: State{}, Top: r, Bottom: r, Box: box}, true
	}
	st, ok := p.parse(s, box, mode)
	if !ok {
		return Lock{}, false
	}
	return Lock{State: st, Top: r, Bottom: box.lastRow, Box: box}, true
}

func (p *codexProfile) Match(s *Screen, st State) (Lock, bool) {
	box, mode, ok := p.geometry(s)
	if !ok || mode != st.Mode || st.Empty() {
		return Lock{}, false
	}
	if cell := s.Cell(box.promptRow, codexTextCol); !cell.Blank() && cell.Attr.Flags&AttrDim != 0 {
		return Lock{}, false
	}
	w := box.width
	rows := WrapCodex(st.Elems, w)
	if len(rows) != box.lastRow-box.promptRow+1 {
		return Lock{}, false
	}
	for i, text := range layoutStrings(st.Elems, rows) {
		if got, _ := rowRun(s, box.promptRow+i, codexTextCol, s.Cols-1); got != text {
			return Lock{}, false
		}
	}
	if row, col := codexCursor(st.Elems, rows, st.Cursor, w); box.promptRow+row != s.Row || codexTextCol+col != s.Col {
		return Lock{}, false
	}
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

func (p *codexProfile) parse(s *Screen, box codexBox, mode string) (State, bool) {
	w := box.width
	n := box.lastRow - box.promptRow + 1
	texts := make([]string, n)
	widths := make([]int, n)
	for i := 0; i < n; i++ {
		texts[i], widths[i] = rowRun(s, box.promptRow+i, codexTextCol, s.Cols-1)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(texts[i])
		if i == n-1 {
			break
		}
		next := texts[i+1]
		first := next
		if j := strings.IndexByte(next, ' '); j >= 0 {
			first = next[:j]
		}
		switch {
		case widths[i] >= w && texts[i+1] == "" && i+1 == n-1:
			// The insertion row after an exactly full line.
		case widths[i] >= w:
		case widths[i] > 0 && first != "" && widths[i]+1+StringWidth(first) > w:
			b.WriteByte(' ')
		default:
			b.WriteByte('\n')
		}
	}
	text := b.String()
	cursorRow, cursorCol := s.Row-box.promptRow, s.Col-codexTextCol
	if cursorRow == n-1 && cursorCol > widths[n-1] {
		text += strings.Repeat(" ", cursorCol-widths[n-1])
	}
	elems := tokenize(text, codexTokenRe)
	rows := WrapCodex(elems, w)
	if len(rows) != n {
		return State{}, false
	}
	got := layoutStrings(elems, rows)
	for i := range got {
		if got[i] != texts[i] {
			return State{}, false
		}
	}
	for c := 0; c <= len(elems); c++ {
		if row, col := codexCursor(elems, rows, c, w); row == cursorRow && col == cursorCol {
			return State{Elems: elems, Cursor: c, Mode: mode}, true
		}
	}
	return State{}, false
}

// codexCursor applies Codex's cursor rule: a cursor past the wrap width moves
// to the next row when that row starts right there, otherwise it is clamped.
func codexCursor(elems []Elem, rows []VisualRow, cursor, width int) (int, int) {
	row, col := cursorRowCol(elems, rows, cursor, width)
	if col >= width {
		if row+1 < len(rows) && rows[row+1].Start == rows[row].End {
			return row + 1, 0
		}
		return row, width - 1
	}
	return row, col
}

func (p *codexProfile) Same(a, b State, s *Screen) bool {
	if a.Mode != b.Mode || a.Empty() != b.Empty() {
		return false
	}
	w := p.textWidth(s.Cols)
	ra, rb := WrapCodex(a.Elems, w), WrapCodex(b.Elems, w)
	if len(ra) != len(rb) {
		return false
	}
	la, lb := layoutStrings(a.Elems, ra), layoutStrings(b.Elems, rb)
	for i := range la {
		if la[i] != lb[i] {
			return false
		}
	}
	xa, ya := codexCursor(a.Elems, ra, a.Cursor, w)
	xb, yb := codexCursor(b.Elems, rb, b.Cursor, w)
	return xa == xb && ya == yb
}

func (p *codexProfile) Render(lock Lock, st State, s *Screen) (Paint, bool) {
	box, ok := lock.Box.(codexBox)
	if !ok || st.Mode != lock.State.Mode {
		return Paint{}, false
	}
	w := box.width
	rows := WrapCodex(st.Elems, w)
	n := len(rows)
	if n > codexMaxRows {
		return Paint{}, false
	}
	footer, ok := p.footerFor(lock, box, st)
	if !ok {
		return Paint{}, false
	}
	// Layout from the bottom of the screen: footer, pad, text rows, pad.
	bottomPad := s.Rows - 1 - len(footer)
	newTop := bottomPad - n
	delta := box.promptRow - newTop // >0: the block's top moves up
	padRow := newTop - 1
	if padRow < 1 {
		return Paint{}, false
	}
	paint := Paint{Rows: map[int][]Cell{}}
	for i, row := range footer {
		paint.Rows[bottomPad+1+i] = append([]Cell(nil), row...)
	}
	paint.Rows[bottomPad] = append([]Cell(nil), box.padBottom...)
	tokenAttr := box.bg
	tokenAttr.FG = Color{ColorIndexed, 6}
	for i, vr := range rows {
		row := blankRow(s.Cols, box.bg)
		if i == 0 {
			row[0], row[1] = box.prompt[0], box.prompt[1]
		}
		if st.Empty() && i == 0 {
			if p.placeholder == nil {
				return Paint{}, false
			}
			copy(row[codexTextCol:], p.placeholder)
		} else {
			rowCells(row, codexTextCol, st.Elems[vr.Start:vr.End], box.bg, tokenAttr)
		}
		paint.Rows[newTop+i] = row
	}
	paint.Rows[padRow] = append([]Cell(nil), box.padTop...)
	// Rows above the block. A short transcript is top-anchored: the blank gap
	// absorbs the change. A full one keeps its newest lines next to the
	// composer, so it shifts.
	oldPad := box.promptRow - 1
	gapBlank := func(from, to int) bool {
		for r := max(0, from); r < to; r++ {
			if !rowBlank(s, r, 0, s.Cols) {
				return false
			}
		}
		return true
	}
	switch {
	case delta > 0 && gapBlank(padRow, oldPad):
		// Growing into blank rows: nothing above moves.
	case delta > 0:
		for r := 0; r < padRow; r++ {
			paint.Rows[r] = copyRow(s, r+delta)
		}
	case delta < 0 && gapBlank(oldPad-1, oldPad):
		for r := oldPad; r < padRow; r++ {
			paint.Rows[r] = blankRow(s.Cols, Attr{})
		}
	case delta < 0:
		for r := oldPad - 1; r >= 0; r-- {
			if r-delta < padRow {
				paint.Rows[r-delta] = copyRow(s, r)
			}
		}
		for r := 0; r < -delta; r++ {
			paint.Rows[r] = blankRow(s.Cols, Attr{})
		}
	}
	crow, ccol := codexCursor(st.Elems, rows, st.Cursor, w)
	if st.Empty() {
		crow, ccol = 0, 0
	}
	paint.CursorRow, paint.CursorCol = newTop+crow, codexTextCol+ccol
	return paint, true
}

func codexIsSeparator(e string) bool {
	return strings.ContainsAny(e, codexSeparator) && len([]rune(e)) == 1
}

func codexWordStart(st State) int {
	i := st.Cursor
	for i > 0 && isSpaceElem(st.Elems[i-1]) {
		i--
	}
	if i == 0 {
		return 0
	}
	sep := codexIsSeparator(st.Elems[i-1].S)
	for i > 0 && !isSpaceElem(st.Elems[i-1]) && codexIsSeparator(st.Elems[i-1].S) == sep && !st.Elems[i-1].Token {
		i--
	}
	if i > 0 && st.Elems[i-1].Token && i == st.Cursor {
		i--
	}
	return i
}

func isSpaceElem(e Elem) bool {
	if e.Token {
		return false
	}
	for _, r := range e.S {
		return unicode.IsSpace(r)
	}
	return false
}

func (p *codexProfile) Edit(k Key, st State, ctx EditContext) EditFunc {
	switch k.Kind {
	case KeyText:
		if !printable(k.Text) {
			return nil
		}
		if st.Cursor == 0 && st.Mode == "" && strings.HasPrefix(k.Text, "!") {
			return nil // shell mode: the ! becomes the prompt
		}
		if st.Empty() && strings.HasPrefix(k.Text, "?") {
			return nil // shortcut overlay
		}
		elems := TextElems(k.Text)
		return func(s State) State { return s.Insert(elems...) }
	case KeyNewline:
		return func(s State) State { return s.Insert(Elem{S: "\n"}) }
	case KeyEnter:
		return p.submit(st)
	case KeyBackspace:
		if st.Cursor == 0 {
			if st.Mode != "" {
				return nil
			}
			return func(s State) State { return s }
		}
		return func(s State) State { return p.dropImage(s.DeleteBackward(), s) }
	case KeyDelete:
		return func(s State) State { return p.dropImage(s.DeleteForward(), s) }
	case KeyLeft:
		if st.Empty() {
			return nil // opens the agents overview
		}
		return func(s State) State { return s.Move(-1) }
	case KeyRight:
		return func(s State) State { return s.Move(1) }
	case KeyHome:
		return func(s State) State { return s.MoveTo(s.LineStart()) }
	case KeyEnd:
		return func(s State) State { return s.MoveTo(s.LineEnd()) }
	case KeyAltBackspace:
		return func(s State) State { return p.dropImage(s.DeleteRange(codexWordStart(s), s.Cursor), s) }
	case KeyCtrl:
		switch k.Rune {
		case 'a':
			if st.Cursor == st.LineStart() && st.Cursor > 0 {
				return nil
			}
			return func(s State) State { return s.MoveTo(s.LineStart()) }
		case 'e':
			if st.Cursor == st.LineEnd() && st.Cursor < len(st.Elems) {
				return nil
			}
			return func(s State) State { return s.MoveTo(s.LineEnd()) }
		case 'b':
			return func(s State) State { return s.Move(-1) }
		case 'f':
			return func(s State) State { return s.Move(1) }
		case 'h':
			if st.Cursor == 0 {
				return func(s State) State { return s }
			}
			return func(s State) State { return p.dropImage(s.DeleteBackward(), s) }
		case 'w':
			return func(s State) State { return p.dropImage(s.DeleteRange(codexWordStart(s), s.Cursor), s) }
		case 'u':
			return func(s State) State {
				from := s.LineStart()
				if from == s.Cursor && from > 0 {
					from--
				}
				return p.dropImage(s.DeleteRange(from, s.Cursor), s)
			}
		case 'k':
			return func(s State) State {
				to := s.LineEnd()
				if to == s.Cursor && to < len(s.Elems) {
					to++
				}
				return p.dropImage(s.DeleteRange(s.Cursor, to), s)
			}
		case 'd':
			if st.Empty() {
				return nil
			}
			return func(s State) State { return p.dropImage(s.DeleteForward(), s) }
		}
		return nil
	case KeyPaste:
		return p.paste(k.Text)
	case KeyImagePaste:
		if !ctx.ImageReady {
			return nil
		}
		return func(s State) State {
			return s.Insert(Elem{S: fmt.Sprintf("[Image #%d]", codexImages(s.Elems)+1), Token: true}, Elem{S: " "})
		}
	}
	return nil
}

// submit predicts Enter: Codex clears the composer synchronously when it
// accepts a submission. Whitespace-only drafts, slash commands, shell input
// and completions in progress are left to the server.
func (p *codexProfile) submit(st State) EditFunc {
	text := strings.TrimSpace(st.Text())
	if text == "" || st.Mode != "" || strings.HasPrefix(text, "/") {
		return nil
	}
	word := st.Cursor
	for word > 0 && !isSpaceElem(st.Elems[word-1]) {
		word--
	}
	if word < st.Cursor && strings.ContainsAny(st.Elems[word].S, "@$/") {
		return nil
	}
	return func(State) State { return State{} }
}

func codexImages(elems []Elem) int {
	n := 0
	for _, e := range elems {
		if e.Token && codexImageRe.MatchString(e.S) {
			n++
		}
	}
	return n
}

// dropImage renumbers image placeholders after one was deleted, as Codex
// keeps them contiguous.
func (p *codexProfile) dropImage(after, before State) State {
	if codexImages(after.Elems) == codexImages(before.Elems) {
		return after
	}
	out := after.Clone()
	n := 0
	for i, e := range out.Elems {
		if e.Token && codexImageRe.MatchString(e.S) {
			n++
			out.Elems[i].S = fmt.Sprintf("[Image #%d]", n)
		}
	}
	return out
}

func (p *codexProfile) paste(raw string) EditFunc {
	text := normalizePaste(raw)
	var b strings.Builder
	for _, r := range text {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	text = b.String()
	if text == "" || looksLikeImagePath(text) || strings.HasPrefix(text, "!") || strings.HasPrefix(text, "> ") {
		return nil
	}
	count := len([]rune(text))
	if count > codexLargePaste {
		base := fmt.Sprintf("[Pasted Content %d chars]", count)
		return func(s State) State {
			suffix := 0
			for _, e := range s.Elems {
				if !e.Token {
					continue
				}
				if e.S == base {
					suffix = max(suffix, 1)
				}
				var k int
				if _, err := fmt.Sscanf(e.S, strings.TrimSuffix(base, "]")+" #%d]", &k); err == nil {
					suffix = max(suffix, k)
				}
			}
			label := base
			if suffix > 0 {
				label = fmt.Sprintf("%s #%d]", strings.TrimSuffix(base, "]"), suffix+1)
			}
			return s.Insert(Elem{S: label, Token: true})
		}
	}
	if !printable(strings.NewReplacer("\n", " ", "\t", " ").Replace(text)) {
		return nil
	}
	elems := TextElems(strings.ReplaceAll(text, "\t", " "))
	return func(s State) State { return s.Insert(elems...) }
}
