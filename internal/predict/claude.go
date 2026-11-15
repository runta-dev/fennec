package predict

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Claude Code's composer (Ink, alternate screen):
//
//	row k-1  notification row: right-aligned hint or status (moves with the box)
//	row k    ──────── top border, full width
//	row k+1  ❯␣text…        (❯ U+276F, ␣ U+00A0; "!␣" in bash mode)
//	row k+2    continuation  (two spaces)
//	row k+n  ──────── bottom border
//
// Text starts at column 2 and wraps with wrap-ansi {hard, trim: false} at
// columns-4. The box is bottom-anchored: growing moves the top border and the
// notification row up into the blank rows above; shrinking leaves blank rows.
// In fullscreen at most max(3, rows/2 - rowsAroundInput) rows are visible,
// with rowsAroundInput >= 5; beyond that the input scrolls internally.
const (
	claudeTextCol       = 2
	claudeHint          = "ctrl+g to edit in Vim"
	claudeTextSlack     = 4
	claudeRowsAround    = 5
	claudeInlinePaste   = 800
	claudeTruncateInput = 10000
)

var (
	claudeTokenRe   = regexp.MustCompile(`\[(?:Pasted text #\d+(?: \+\d+ lines)?|Image #\d+|\.\.\.Truncated text #\d+ \+\d+ lines\.\.\.)\]`)
	claudeNumberRe  = regexp.MustCompile(`#(\d+)`)
	claudeImageRe   = regexp.MustCompile(`^\[Image #\d+\]$`)
	claudeHintAttr  = Attr{FG: Color{ColorRGB, 0x999999}}
	claudeNoSpaceRe = regexp.MustCompile(`^[\s.,?!:;)\]]`)
)

type claudeBox struct {
	prompt       [2]Cell
	topBorder    []Cell
	chrome       []Cell
	chromeRow    int
	hasChrome    bool
	blockTop     int // first row of the box: notification row or top border
	width        int
	promptRow    int
	lastRow      int
	bottomBorder int
}

type claudeProfile struct {
	// placeholder is what an empty composer shows from the text column: the
	// dim "Try …" suggestion on a fresh session, blanks afterwards. nil until
	// an empty composer has been seen.
	placeholder []Cell
	maxID       int // highest paste or image number seen this session
	width, rows int // text width and screen rows at the last lock
	// notice is the notification row as last seen without the multi-line
	// hint; Claude shows it again when the hint goes away.
	notice []Cell
	// region is the transcript area of the last frame; history holds rows
	// that scrolled off the top, oldest first.
	region, history [][]Cell
}

const claudeHistory = 200

func newClaudeProfile() *claudeProfile { return &claudeProfile{} }

func (p *claudeProfile) Name() string { return "claude" }

func isClaudeBorder(s *Screen, r int) bool {
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

func claudePromptMode(s *Screen, r int) (string, bool) {
	if s.Cell(r, 1).Text != " " {
		return "", false
	}
	switch s.Cell(r, 0).Text {
	case "❯":
		return "", true
	case "!":
		return "bash", true
	}
	return "", false
}

func claudeContinuation(s *Screen, r int) bool {
	return s.Cell(r, 0).Blank() && s.Cell(r, 1).Blank() && !isClaudeBorder(s, r)
}

func (p *claudeProfile) textWidth(cols int) int { return max(1, cols-claudeTextSlack) }

func (p *claudeProfile) maxRows(rows int) int { return max(3, rows/2-claudeRowsAround) }

func (p *claudeProfile) Locate(s *Screen) (Lock, bool) {
	box, mode, ok := p.geometry(s)
	if !ok {
		return Lock{}, false
	}
	// Dim text in the text area is the fresh-session placeholder, or ghost
	// text (prompt suggestions) that cannot be modelled.
	if p.dimText(s, box) {
		if box.lastRow != box.promptRow || s.Row != box.promptRow || s.Col != claudeTextCol {
			return Lock{}, false
		}
		p.placeholder = append([]Cell(nil), s.grid[box.promptRow][claudeTextCol:]...)
		return Lock{State: State{Mode: mode}, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
	}
	st, ok := p.parse(s, box, mode)
	if !ok {
		return Lock{}, false
	}
	if st.Empty() {
		p.placeholder = append([]Cell(nil), s.grid[box.promptRow][claudeTextCol:]...)
	}
	if n := maxTokenNumber(st.Elems, claudeNumberRe); n > p.maxID {
		p.maxID = n
	}
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

func (p *claudeProfile) Match(s *Screen, st State) (Lock, bool) {
	box, mode, ok := p.geometry(s)
	if !ok || mode != st.Mode || st.Empty() || p.dimText(s, box) {
		return Lock{}, false
	}
	w := box.width
	rows := claudeWrap(st.Elems, w)
	if len(rows) != box.lastRow-box.promptRow+1 {
		return Lock{}, false
	}
	for i, text := range layoutStrings(st.Elems, rows) {
		if got, _ := rowRun(s, box.promptRow+i, claudeTextCol, claudeTextCol+w); got != text {
			return Lock{}, false
		}
	}
	if row, col := cursorRowCol(st.Elems, rows, st.Cursor, w); box.promptRow+row != s.Row || claudeTextCol+col != s.Col {
		return Lock{}, false
	}
	return Lock{State: st, Top: box.promptRow, Bottom: box.lastRow, Box: box}, true
}

func (p *claudeProfile) dimText(s *Screen, box claudeBox) bool {
	for row := box.promptRow; row <= box.lastRow; row++ {
		for c := claudeTextCol; c < s.Cols; c++ {
			if cell := s.Cell(row, c); !cell.Blank() && cell.Attr.Flags&AttrDim != 0 {
				return true
			}
		}
	}
	return false
}

// geometry finds the composer box around the cursor.
func (p *claudeProfile) geometry(s *Screen) (claudeBox, string, bool) {
	fail := func() (claudeBox, string, bool) { return claudeBox{}, "", false }
	if !s.CursorVisible || s.Cols < 12 {
		return fail()
	}
	r := s.Row
	for r >= 0 {
		if _, ok := claudePromptMode(s, r); ok {
			break
		}
		if !claudeContinuation(s, r) {
			return fail()
		}
		r--
	}
	if r < 1 || !isClaudeBorder(s, r-1) {
		return fail()
	}
	mode, _ := claudePromptMode(s, r)
	box := claudeBox{promptRow: r, width: p.textWidth(s.Cols)}
	box.lastRow = r
	for box.lastRow+1 < s.Rows && claudeContinuation(s, box.lastRow+1) {
		box.lastRow++
	}
	if !isClaudeBorder(s, box.lastRow+1) || box.lastRow-box.promptRow+1 > p.maxRows(s.Rows) {
		return fail()
	}
	box.bottomBorder = box.lastRow + 1
	if s.Row < box.promptRow || s.Row > box.lastRow || s.Col < claudeTextCol {
		return fail()
	}
	box.prompt = [2]Cell{s.Cell(r, 0), s.Cell(r, 1)}
	box.topBorder = copyRow(s, r-1)
	box.blockTop = r - 1
	// The row above the border is always the notification row: blank, a
	// status notice, or the multi-line hint. It moves with the box.
	if r-2 >= 0 {
		if !rowBlank(s, r-2, 0, s.Cols/3) {
			return fail() // transcript text where the notification row belongs
		}
		box.chromeRow, box.hasChrome, box.chrome = r-2, true, copyRow(s, r-2)
		box.blockTop = r - 2
		if !strings.Contains(s.RowText(r-2, 0, s.Cols), claudeHint) {
			p.notice = box.chrome
		}
	}
	for row := box.promptRow; row <= box.lastRow; row++ {
		if !rowBlank(s, row, claudeTextCol+box.width+1, s.Cols) {
			return fail()
		}
	}
	p.width, p.rows = box.width, s.Rows
	p.track(s, box.blockTop)
	return box, mode, true
}

// track remembers transcript rows that scroll off the top of the screen, so
// a predicted shrink can bring them back. It compares the transcript area
// with the previous frame's: a consistent shift up pushes the rows that left,
// a shift down pops the rows that returned, anything else forgets.
func (p *claudeProfile) track(s *Screen, top int) {
	cur := make([][]Cell, top)
	for r := range cur {
		cur[r] = s.grid[r]
	}
	prev := p.region
	p.region = make([][]Cell, top)
	for r := range cur {
		p.region[r] = append([]Cell(nil), cur[r]...)
	}
	if len(prev) == 0 || len(prev[0]) != s.Cols {
		p.history = nil
		return
	}
	shift, ok := regionShift(prev, cur)
	switch {
	case !ok:
		p.history = nil
	case shift > 0:
		p.history = append(p.history, prev[:shift]...)
		if len(p.history) > claudeHistory {
			p.history = p.history[len(p.history)-claudeHistory:]
		}
	case shift < 0:
		p.history = p.history[:max(0, len(p.history)+shift)]
	}
}

// regionShift finds s with cur[r] == prev[r+s] over their overlap (at least
// three rows, some not blank). 0 means the rows are where they were.
func regionShift(prev, cur [][]Cell) (int, bool) {
	same := func(a, b []Cell) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	try := func(shift int) bool {
		overlap, content := 0, false
		for r := range cur {
			q := r + shift
			if q < 0 || q >= len(prev) {
				continue
			}
			if !same(cur[r], prev[q]) {
				return false
			}
			overlap++
			for _, c := range cur[r] {
				if !c.Blank() {
					content = true
					break
				}
			}
		}
		return overlap >= 3 && content
	}
	limit := max(len(prev), len(cur))
	for d := 0; d < limit; d++ {
		if try(d) {
			return d, true
		}
		if d > 0 && try(-d) {
			return -d, true
		}
	}
	return 0, false
}

// parse reads the text rows back into a state and verifies it by laying it out
// again.
func (p *claudeProfile) parse(s *Screen, box claudeBox, mode string) (State, bool) {
	w := box.width
	n := box.lastRow - box.promptRow + 1
	texts := make([]string, n)
	widths := make([]int, n)
	for i := 0; i < n; i++ {
		texts[i], widths[i] = rowRun(s, box.promptRow+i, claudeTextCol, claudeTextCol+w)
	}
	// Separator between each pair of rows. After an exactly full row the
	// next row either continues a hard-broken word or starts after a space
	// that the renderer hides; both are tried and the layout decides.
	seps := make([][]string, n-1)
	ambiguous := 0
	for i := 0; i < n-1; i++ {
		next := texts[i+1]
		first := next
		if j := strings.IndexByte(next, ' '); j >= 0 {
			first = next[:j]
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
	cursorRow, cursorCol := s.Row-box.promptRow, s.Col-claudeTextCol
	verify := func(text string) (State, bool) {
		// Spaces typed at the end are invisible; the cursor shows them.
		if cursorRow == n-1 && cursorCol > widths[n-1] {
			text += strings.Repeat(" ", cursorCol-widths[n-1])
		}
		elems := tokenize(text, claudeTokenRe)
		rows := claudeWrap(elems, w)
		if len(rows) != n {
			return State{}, false
		}
		got := layoutStrings(elems, rows)
		for i := range got {
			if got[i] != texts[i] {
				return State{}, false
			}
		}
		// Positions inside hidden whitespace share a screen cell; the cursor
		// is after the space it just typed, so prefer the later index.
		for c := len(elems); c >= 0; c-- {
			if row, col := cursorRowCol(elems, rows, c, w); row == cursorRow && col == cursorCol {
				return State{Elems: elems, Cursor: c, Mode: mode}, true
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

// claudeWrap is wrap-ansi plus Claude Code's rendering rule: a row that
// starts by soft wrap does not draw its leading whitespace (a space typed
// after an exactly full row starts the next row invisibly, and the cursor
// after it is at column 0).
func claudeWrap(elems []Elem, w int) []VisualRow {
	rows := WrapAnsi(elems, w)
	for i := 1; i < len(rows); i++ {
		r := &rows[i]
		if r.Start == 0 || r.Start > len(elems) || elems[r.Start-1].S == "\n" {
			continue
		}
		for r.Start+r.Hidden < r.End && !elems[r.Start+r.Hidden].Token && elems[r.Start+r.Hidden].S == " " {
			r.Width -= elemWidth(elems[r.Start+r.Hidden])
			r.Hidden++
		}
	}
	return rows
}

func (p *claudeProfile) Same(a, b State, s *Screen) bool {
	if a.Mode != b.Mode || a.Empty() != b.Empty() {
		return false
	}
	w := p.textWidth(s.Cols)
	ra, rb := claudeWrap(a.Elems, w), claudeWrap(b.Elems, w)
	if len(ra) != len(rb) {
		return false
	}
	la, lb := layoutStrings(a.Elems, ra), layoutStrings(b.Elems, rb)
	for i := range la {
		// Token numbers are the agent's to assign; compare tokens by kind.
		if claudeNumberRe.ReplaceAllString(la[i], "#N") != claudeNumberRe.ReplaceAllString(lb[i], "#N") {
			return false
		}
	}
	xa, ya := cursorRowCol(a.Elems, ra, a.Cursor, w)
	xb, yb := cursorRowCol(b.Elems, rb, b.Cursor, w)
	return xa == xb && ya == yb
}

func (p *claudeProfile) Render(lock Lock, st State, s *Screen) (Paint, bool) {
	box, ok := lock.Box.(claudeBox)
	if !ok || st.Mode != lock.State.Mode {
		return Paint{}, false
	}
	w := box.width
	rows := claudeWrap(st.Elems, w)
	n := len(rows)
	if n > p.maxRows(s.Rows) {
		return Paint{}, false
	}
	// The notification row shows the multi-line hint while the text has a
	// newline; when the hint goes, the notice it covered (or blank) returns.
	hasHint := box.hasChrome && strings.Contains(s.RowText(box.chromeRow, 0, s.Cols), claudeHint)
	wantHint := strings.Contains(st.Text(), "\n")
	var notice []Cell
	switch {
	case !box.hasChrome:
	case wantHint && hasHint, !wantHint && !hasHint:
		notice = box.chrome
	case wantHint:
		notice = blankRow(s.Cols, Attr{})
		if start := s.Cols - 2 - len(claudeHint); start >= 0 {
			rowCells(notice, start, TextElems(claudeHint), claudeHintAttr, claudeHintAttr)
		}
	default:
		notice = blankRow(s.Cols, Attr{})
		if len(p.notice) == s.Cols {
			notice = p.notice
		}
	}
	newTop := box.lastRow - (n - 1)
	newBlock := newTop - 1 // top border
	if notice != nil {
		newBlock--
	}
	if newBlock < 0 {
		return Paint{}, false
	}
	delta := box.blockTop - newBlock // >0: the box grows upward
	paint := Paint{Rows: map[int][]Cell{}}
	// The transcript above is a viewport anchored to the box. A short
	// transcript is top-aligned with a blank gap below it, which growth uses
	// first; a full one is bottom-aligned and scrolls with the box.
	gap := 0
	for r := box.blockTop - 1; r >= 0 && rowBlank(s, r, 0, s.Cols); r-- {
		gap++
	}
	switch {
	case delta > 0:
		if scroll := delta - min(gap, delta); scroll > 0 {
			for r := 0; r < newBlock; r++ {
				paint.Rows[r] = copyRow(s, r+scroll)
			}
		}
	case delta < 0 && gap > 0:
		for r := box.blockTop; r < newBlock; r++ {
			paint.Rows[r] = blankRow(s.Cols, Attr{})
		}
	case delta < 0:
		// Scroll back down; the rows revealed at the top are the ones that
		// scrolled off earlier, when the profile saw them.
		d := -delta
		for r := newBlock - 1; r >= d; r-- {
			paint.Rows[r] = copyRow(s, r-d)
		}
		for r := 0; r < d; r++ {
			row := blankRow(s.Cols, Attr{})
			if i := len(p.history) - d + r; i >= 0 && len(p.history[i]) == s.Cols {
				row = append([]Cell(nil), p.history[i]...)
			}
			paint.Rows[r] = row
		}
	}
	for i, vr := range rows {
		row := blankRow(s.Cols, Attr{})
		if i == 0 {
			row[0], row[1] = box.prompt[0], box.prompt[1]
		}
		if st.Empty() && i == 0 {
			if p.placeholder == nil {
				return Paint{}, false
			}
			copy(row[claudeTextCol:], p.placeholder)
		} else {
			rowCells(row, claudeTextCol, st.Elems[vr.Start+vr.Hidden:vr.End], Attr{}, Attr{})
		}
		paint.Rows[newTop+i] = row
	}
	// Border and notification row are painted only when they move or the
	// notification changes; otherwise the server keeps them current
	// (notices time out on their own).
	if delta != 0 {
		paint.Rows[newTop-1] = append([]Cell(nil), box.topBorder...)
	}
	if notice != nil && (delta != 0 || !box.hasChrome || hasHint != wantHint) {
		paint.Rows[newBlock] = append([]Cell(nil), notice...)
	}
	crow, ccol := cursorRowCol(st.Elems, rows, st.Cursor, w)
	if st.Empty() {
		crow, ccol = 0, 0
	}
	paint.CursorRow, paint.CursorCol = newTop+crow, claudeTextCol+ccol
	return paint, true
}

// nextID continues Claude Code's session-wide paste counter: the highest
// number seen on screen this session or already in the predicted draft.
func (p *claudeProfile) nextID(st State) int {
	return max(p.maxID, maxTokenNumber(st.Elems, claudeNumberRe)) + 1
}

// visual returns the wrapped rows of st and the cursor's row and column.
func (p *claudeProfile) visual(st State) ([]VisualRow, int, int) {
	rows := claudeWrap(st.Elems, p.width)
	row, col := cursorRowCol(st.Elems, rows, st.Cursor, p.width)
	return rows, row, col
}

// atColumn finds the element index in row whose start is closest to col
// without passing it.
func atColumn(st State, row VisualRow, col int) int {
	x := 0
	for i := row.Start; i < row.End; i++ {
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

// rowEnd is the last insertion point of a visual row (before its newline).
func rowEnd(s State, row VisualRow) int {
	end := row.End
	if end > row.Start && end <= len(s.Elems) && s.Elems[end-1].S == "\n" {
		end--
	}
	return end
}

func (p *claudeProfile) Edit(k Key, st State, ctx EditContext) EditFunc {
	if p.width == 0 {
		return nil
	}
	switch k.Kind {
	case KeyText:
		if !printable(k.Text) {
			return nil
		}
		if st.Cursor == 0 && st.Mode == "" && strings.HasPrefix(k.Text, "!") {
			return nil // bash mode: the ! becomes the prompt
		}
		if st.Empty() && strings.HasPrefix(k.Text, "?") {
			return nil // shortcut help
		}
		text := k.Text
		// After an image pill, typed text is separated by a space.
		if st.Cursor > 0 && st.Elems[st.Cursor-1].Token && claudeImageRe.MatchString(st.Elems[st.Cursor-1].S) && !claudeNoSpaceRe.MatchString(text) {
			text = " " + text
		}
		elems := TextElems(text)
		return func(s State) State { return s.Insert(elems...) }
	case KeyNewline:
		return func(s State) State { return s.Insert(Elem{S: "\n"}) }
	case KeyEnter:
		if st.Cursor > 0 && st.Elems[st.Cursor-1].S == "\\" {
			return func(s State) State { return s.DeleteBackward().Insert(Elem{S: "\n"}) }
		}
		return p.submit(st)
	case KeyBackspace:
		if st.Cursor == 0 {
			if st.Mode != "" {
				return nil
			}
			return func(s State) State { return s }
		}
		return func(s State) State { return s.DeleteBackward() }
	case KeyDelete:
		return func(s State) State { return s.DeleteForward() }
	case KeyLeft:
		if st.Empty() {
			return nil
		}
		return func(s State) State { return s.Move(-1) }
	case KeyRight:
		return func(s State) State { return s.Move(1) }
	case KeyUp, KeyDown:
		step := -1
		if k.Kind == KeyDown {
			step = 1
		}
		rows, row, _ := p.visual(st)
		if row+step < 0 || row+step >= len(rows) {
			return nil // history
		}
		return func(s State) State {
			rows, row, col := p.visual(s)
			if row+step < 0 || row+step >= len(rows) {
				return s
			}
			return s.MoveTo(atColumn(s, rows[row+step], col))
		}
	case KeyHome:
		return func(s State) State {
			rows, row, _ := p.visual(s)
			if s.Cursor == rows[row].Start && row > 0 {
				return s.MoveTo(rows[row-1].Start)
			}
			return s.MoveTo(rows[row].Start)
		}
	case KeyEnd:
		return func(s State) State {
			rows, row, _ := p.visual(s)
			end := rowEnd(s, rows[row])
			if s.Cursor == end && row+1 < len(rows) {
				end = rowEnd(s, rows[row+1])
			}
			return s.MoveTo(end)
		}
	case KeyAltBackspace:
		return func(s State) State { return s.DeleteRange(s.WordStartBefore(IsAlnumWord), s.Cursor) }
	case KeyCtrl:
		switch k.Rune {
		case 'a':
			return func(s State) State { return s.MoveTo(s.LineStart()) }
		case 'e':
			return func(s State) State { return s.MoveTo(s.LineEnd()) }
		case 'b':
			return func(s State) State { return s.Move(-1) }
		case 'f':
			return func(s State) State { return s.Move(1) }
		case 'h':
			if st.Cursor == 0 {
				return nil
			}
			return func(s State) State { return s.DeleteBackward() }
		case 'w':
			return func(s State) State { return s.DeleteRange(s.WordStartBefore(IsNonSpaceWord), s.Cursor) }
		case 'k':
			return func(s State) State {
				if s.Cursor < len(s.Elems) && s.Elems[s.Cursor].S == "\n" {
					return s.DeleteForward()
				}
				rows, row, _ := p.visual(s)
				return s.DeleteRange(s.Cursor, rowEnd(s, rows[row]))
			}
		case 'u':
			if st.Cursor == 0 && st.Mode != "" {
				return nil
			}
			return func(s State) State {
				if s.Cursor > 0 && s.Elems[s.Cursor-1].S == "\n" {
					return s.DeleteBackward()
				}
				rows, row, _ := p.visual(s)
				return s.DeleteRange(rows[row].Start, s.Cursor)
			}
		case 'c':
			if st.Empty() {
				return nil // "Press Ctrl-C again to exit"
			}
			return func(s State) State { return State{Mode: s.Mode} }
		}
		return nil
	case KeyPaste:
		return p.paste(k.Text, st)
	case KeyImagePaste:
		if !ctx.ImageReady {
			return nil
		}
		token := Elem{S: fmt.Sprintf("[Image #%d]", p.nextID(st)), Token: true}
		return func(s State) State { return s.Insert(token) }
	}
	return nil
}

// submit predicts Enter: the composer clears at once (the message is sent or
// queued). Slash commands, shell input, mentions being completed and drafts
// over the truncation limit are left to the server.
func (p *claudeProfile) submit(st State) EditFunc {
	text := strings.TrimSpace(st.Text())
	if text == "" || st.Mode != "" || strings.HasPrefix(text, "/") || utf16Len(st.Text()) > claudeTruncateInput {
		return nil
	}
	word := st.Cursor
	for word > 0 && !isSpaceElem(st.Elems[word-1]) {
		word--
	}
	if word < st.Cursor && (st.Elems[word].S == "@" || st.Elems[word].S == "/") {
		return nil
	}
	cols := 0
	if p.placeholder != nil {
		cols = len(p.placeholder)
	}
	return func(s State) State {
		// After the first message the empty composer shows no suggestion.
		if cols > 0 {
			p.placeholder = blankRow(cols, Attr{})
		}
		return State{}
	}
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// paste follows Claude Code's rule: a paste becomes a pill when it is longer
// than 800 UTF-16 units or has more than min(rows-10, 2) newlines.
func (p *claudeProfile) paste(raw string, st State) EditFunc {
	text := strings.ReplaceAll(normalizePaste(raw), "\t", "    ")
	if text == "" || looksLikeImagePath(text) {
		return nil
	}
	lines := countNewlines(text)
	if utf16Len(text) <= claudeInlinePaste && lines <= max(0, min(p.rows-10, 2)) {
		if !printable(strings.ReplaceAll(text, "\n", " ")) {
			return nil
		}
		if st.Cursor == 0 && st.Mode == "" && strings.HasPrefix(text, "!") {
			return nil
		}
		elems := TextElems(text)
		return func(s State) State { return s.Insert(elems...) }
	}
	if utf16Len(st.Text())+utf16Len(text) > claudeTruncateInput {
		return nil
	}
	id := p.nextID(st)
	label := fmt.Sprintf("[Pasted text #%d]", id)
	if lines > 0 {
		label = fmt.Sprintf("[Pasted text #%d +%d lines]", id, lines)
	}
	token := Elem{S: label, Token: true}
	return func(s State) State { return s.Insert(token) }
}
