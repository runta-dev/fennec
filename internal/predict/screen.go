package predict

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Screen is a VT emulator limited to what herdr emits for the supported agents.
// It models cell contents, attributes, the cursor and the modes that change how
// later bytes are interpreted. It never answers queries; the real terminal does.
type Screen struct {
	Rows, Cols int

	grid      [][]Cell
	main, alt [][]Cell
	AltActive bool

	Row, Col    int
	PendingWrap bool
	Pen         Attr
	// PenLink is the open OSC 8 hyperlink ("params;URI", empty: none). Like
	// xterm.js, SGR does not change it and erased cells carry none.
	PenLink string

	CursorVisible bool
	OriginMode    bool
	AutoWrap      bool
	InsertMode    bool
	Sync          bool // DEC mode 2026 is open: the frame is incomplete

	Top, Bottom int // scroll region, inclusive

	G0Graphics, G1Graphics, ShiftOut bool

	savedMain, savedAlt savedCursor

	// Mutations reports structural changes that invalidate composer
	// coordinates (scrolls, screen switches, full clears).
	Scrolled bool

	state   parseState
	params  []byte
	inter   []byte
	utf8buf []byte
	lastCh  string
	strEsc  bool
	// osc collects an OSC string (nil for other strings); oscOver: it
	// exceeded MaxLinkBytes.
	osc     []byte
	oscOver bool
}

type savedCursor struct {
	valid                 bool
	row, col              int
	pen                   Attr
	link                  string
	origin, wrapPending   bool
	g0Graphics, g1Graphic bool
	shiftOut              bool
}

type parseState uint8

const (
	stGround parseState = iota
	stEscape
	stEscInter
	stCSI
	stString // OSC, DCS, APC, PM, SOS: consumed until BEL or ST
)

// Cell holds one grapheme. Width 0 marks the trailing half of a wide glyph.
// Link is the OSC 8 hyperlink it was printed with ("params;URI").
type Cell struct {
	Text  string
	Width uint8
	Attr  Attr
	Link  string
}

// MaxLinkBytes bounds an OSC 8 string the screen models; a longer link is
// dropped (the cells print without it), so output containing one must not be
// reproduced from the screen.
const MaxLinkBytes = 4096

func (c Cell) Blank() bool { return (c.Text == "" || c.Text == " ") && c.Width != 0 }

// Glyph returns the visible text of the cell, a space for blanks.
func (c Cell) Glyph() string {
	if c.Text == "" {
		return " "
	}
	return c.Text
}

type ColorKind uint8

const (
	ColorDefault ColorKind = iota
	ColorIndexed
	ColorRGB
)

type Color struct {
	Kind  ColorKind
	Value uint32
}

const (
	AttrBold uint16 = 1 << iota
	AttrDim
	AttrItalic
	AttrUnderline
	AttrBlink
	AttrInverse
	AttrHidden
	AttrStrike
)

type Attr struct {
	FG, BG Color
	Flags  uint16
}

func blankCell(pen Attr) Cell { return Cell{Width: 1, Attr: Attr{BG: pen.BG}} }

func NewScreen(rows, cols int) *Screen {
	s := &Screen{}
	s.Reset(rows, cols)
	return s
}

// Reset performs a full reset (RIS) at the given size.
func (s *Screen) Reset(rows, cols int) {
	rows, cols = max(1, rows), max(1, cols)
	*s = Screen{Rows: rows, Cols: cols, CursorVisible: true, AutoWrap: true, Bottom: rows - 1}
	s.main = newGrid(rows, cols)
	s.alt = newGrid(rows, cols)
	s.grid = s.main
	s.Scrolled = true
}

func newGrid(rows, cols int) [][]Cell {
	g := make([][]Cell, rows)
	for r := range g {
		g[r] = newRow(cols, Attr{})
	}
	return g
}

func newRow(cols int, pen Attr) []Cell {
	row := make([]Cell, cols)
	for i := range row {
		row[i] = blankCell(pen)
	}
	return row
}

// Clone returns an independent copy, including parser state.
func (s *Screen) Clone() *Screen {
	c := *s
	c.main = cloneGrid(s.main)
	c.alt = cloneGrid(s.alt)
	if s.AltActive {
		c.grid = c.alt
	} else {
		c.grid = c.main
	}
	c.params = append([]byte(nil), s.params...)
	c.inter = append([]byte(nil), s.inter...)
	c.utf8buf = append([]byte(nil), s.utf8buf...)
	if s.osc != nil {
		c.osc = append([]byte{}, s.osc...)
	}
	return &c
}

func cloneGrid(g [][]Cell) [][]Cell {
	out := make([][]Cell, len(g))
	for i, row := range g {
		out[i] = append([]Cell(nil), row...)
	}
	return out
}

// Resize keeps the top-left contents, as a terminal without reflow does.
func (s *Screen) Resize(rows, cols int) {
	rows, cols = max(1, rows), max(1, cols)
	resize := func(g [][]Cell) [][]Cell {
		out := newGrid(rows, cols)
		for r := 0; r < min(rows, len(g)); r++ {
			copy(out[r], g[r][:min(cols, len(g[r]))])
		}
		return out
	}
	s.main, s.alt = resize(s.main), resize(s.alt)
	if s.AltActive {
		s.grid = s.alt
	} else {
		s.grid = s.main
	}
	s.Rows, s.Cols = rows, cols
	s.Top, s.Bottom = 0, rows-1
	s.Row, s.Col = min(s.Row, rows-1), min(s.Col, cols-1)
	s.PendingWrap = false
	s.Scrolled = true
}

func (s *Screen) Cell(row, col int) Cell {
	if row < 0 || row >= s.Rows || col < 0 || col >= s.Cols {
		return Cell{Width: 1}
	}
	return s.grid[row][col]
}

func (s *Screen) SetCell(row, col int, c Cell) {
	if row >= 0 && row < s.Rows && col >= 0 && col < s.Cols {
		s.grid[row][col] = c
	}
}

// RowText returns the visible text of a row between two columns, wide glyphs
// once, blanks as spaces.
func (s *Screen) RowText(row, from, to int) string {
	var b strings.Builder
	for col := max(0, from); col < min(to, s.Cols); col++ {
		c := s.Cell(row, col)
		if c.Width == 0 {
			continue
		}
		b.WriteString(c.Glyph())
	}
	return b.String()
}

// Ground reports whether the parser sits between sequences and the server is
// not inside a synchronized update, so the frame is complete.
func (s *Screen) Ground() bool {
	return s.state == stGround && len(s.utf8buf) == 0 && !s.Sync
}

// Write feeds terminal output.
func (s *Screen) Write(data []byte) {
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch s.state {
		case stString:
			if b == 0x07 || (s.strEsc && b == '\\') {
				s.state, s.strEsc = stGround, false
				s.endOSC()
				continue
			}
			if s.strEsc && s.osc != nil && !s.oscOver {
				s.osc = append(s.osc, 0x1b) // not the start of ST after all
			}
			s.strEsc = b == 0x1b
			if s.osc != nil && !s.strEsc && !s.oscOver {
				if len(s.osc) >= MaxLinkBytes+2 {
					s.oscOver = true
				} else {
					s.osc = append(s.osc, b)
				}
			}
			continue
		case stEscape:
			s.escape(b)
			continue
		case stEscInter:
			if b >= 0x20 && b <= 0x2f {
				s.inter = append(s.inter, b)
				continue
			}
			s.escDispatch(b)
			continue
		case stCSI:
			switch {
			case b >= 0x30 && b <= 0x3f:
				s.params = append(s.params, b)
			case b >= 0x20 && b <= 0x2f:
				s.inter = append(s.inter, b)
			case b >= 0x40 && b <= 0x7e:
				s.state = stGround
				s.csi(b)
			case b == 0x1b:
				s.state = stEscape
			case b < 0x20:
				s.control(b)
			default:
				s.state = stGround
			}
			continue
		}
		// Ground state.
		if len(s.utf8buf) > 0 {
			if b&0xc0 == 0x80 {
				s.utf8buf = append(s.utf8buf, b)
				if utf8.FullRune(s.utf8buf) {
					r, _ := utf8.DecodeRune(s.utf8buf)
					s.utf8buf = s.utf8buf[:0]
					s.print(r)
				}
				continue
			}
			s.utf8buf = s.utf8buf[:0]
			s.print(utf8.RuneError)
		}
		switch {
		case b == 0x1b:
			s.state = stEscape
			s.params, s.inter = s.params[:0], s.inter[:0]
		case b < 0x20 || b == 0x7f:
			s.control(b)
		case b < 0x80:
			s.print(rune(b))
		default:
			s.utf8buf = append(s.utf8buf, b)
			if utf8.FullRune(s.utf8buf) {
				r, _ := utf8.DecodeRune(s.utf8buf)
				s.utf8buf = s.utf8buf[:0]
				s.print(r)
			}
		}
	}
}

func (s *Screen) control(b byte) {
	switch b {
	case 0x08:
		s.PendingWrap = false
		if s.Col > 0 {
			s.Col--
		}
	case 0x09:
		s.PendingWrap = false
		s.Col = min(s.Cols-1, (s.Col/8+1)*8)
	case 0x0a, 0x0b, 0x0c:
		s.index()
	case 0x0d:
		s.PendingWrap = false
		s.Col = 0
	case 0x0e:
		s.ShiftOut = true
	case 0x0f:
		s.ShiftOut = false
	}
}

func (s *Screen) escape(b byte) {
	s.state = stGround
	switch b {
	case '[':
		s.state = stCSI
		s.params, s.inter = s.params[:0], s.inter[:0]
	case ']':
		s.state, s.strEsc = stString, false
		s.osc, s.oscOver = []byte{}, false
	case 'P', '_', '^', 'X':
		s.state, s.strEsc = stString, false
		s.osc = nil
	case '7':
		s.saveCursor()
	case '8':
		s.restoreCursor()
	case 'D':
		s.index()
	case 'E':
		s.index()
		s.Col = 0
	case 'M':
		s.reverseIndex()
	case 'c':
		s.Reset(s.Rows, s.Cols)
	case '\\':
	default:
		if b >= 0x20 && b <= 0x2f {
			s.inter = append(s.inter[:0], b)
			s.state = stEscInter
		}
	}
}

func (s *Screen) escDispatch(final byte) {
	s.state = stGround
	if len(s.inter) != 1 {
		return
	}
	switch s.inter[0] {
	case '(':
		s.G0Graphics = final == '0'
	case ')':
		s.G1Graphics = final == '0'
	}
}

var decGraphics = map[rune]string{
	'j': "┘", 'k': "┐", 'l': "┌", 'm': "└", 'n': "┼", 'q': "─", 't': "├", 'u': "┤", 'v': "┴", 'w': "┬", 'x': "│",
	'a': "▒", '`': "◆", 'f': "°", 'g': "±", '~': "·", 'o': "⎺", 's': "⎽", 'y': "≤", 'z': "≥", '{': "π", '|': "≠", '}': "£",
}

func (s *Screen) print(r rune) {
	text := string(r)
	graphics := (s.ShiftOut && s.G1Graphics) || (!s.ShiftOut && s.G0Graphics)
	if graphics {
		if mapped, ok := decGraphics[r]; ok {
			text = mapped
		}
	}
	w := RuneWidth(r)
	if w == 0 {
		s.combine(text)
		return
	}
	if text == " " {
		text = "" // a printed space and an erased cell look the same
	}
	if s.PendingWrap && s.AutoWrap {
		s.PendingWrap = false
		s.Col = 0
		s.index()
	}
	if w == 2 && s.Col == s.Cols-1 {
		if s.AutoWrap {
			s.SetCell(s.Row, s.Col, blankCell(s.Pen))
			s.Col = 0
			s.index()
		} else {
			return
		}
	}
	if s.InsertMode {
		s.insertBlank(w)
	}
	s.clearWideAt(s.Row, s.Col)
	if w == 2 {
		s.clearWideAt(s.Row, s.Col+1)
	}
	s.grid[s.Row][s.Col] = Cell{Text: text, Width: uint8(w), Attr: s.Pen, Link: s.PenLink}
	if w == 2 {
		s.grid[s.Row][s.Col+1] = Cell{Width: 0, Attr: s.Pen, Link: s.PenLink}
	}
	s.lastCh = text
	if s.Col+w >= s.Cols {
		s.Col = s.Cols - 1
		s.PendingWrap = s.AutoWrap
		if w == 2 {
			s.Col = s.Cols - 1
		}
		return
	}
	s.Col += w
}

// clearWideAt keeps wide glyphs whole when one half is overwritten.
func (s *Screen) clearWideAt(row, col int) {
	if col < 0 || col >= s.Cols {
		return
	}
	c := s.grid[row][col]
	if c.Width == 0 && col > 0 {
		s.grid[row][col-1] = blankCell(c.Attr)
	}
	if c.Width == 2 && col+1 < s.Cols {
		s.grid[row][col+1] = blankCell(c.Attr)
	}
}

func (s *Screen) combine(text string) {
	row, col := s.Row, s.Col
	if !s.PendingWrap {
		col--
	}
	if col >= 0 && s.grid[row][col].Width == 0 && col > 0 {
		col--
	}
	if col < 0 {
		return
	}
	c := &s.grid[row][col]
	if c.Text == "" {
		c.Text = " "
	}
	c.Text += text
}

func (s *Screen) index() {
	s.PendingWrap = false
	if s.Row == s.Bottom {
		s.scrollUp(s.Top, s.Bottom, 1)
		return
	}
	if s.Row < s.Rows-1 {
		s.Row++
	}
}

func (s *Screen) reverseIndex() {
	s.PendingWrap = false
	if s.Row == s.Top {
		s.scrollDown(s.Top, s.Bottom, 1)
		return
	}
	if s.Row > 0 {
		s.Row--
	}
}

func (s *Screen) scrollUp(top, bottom, n int) {
	s.Scrolled = true
	n = min(n, bottom-top+1)
	for i := 0; i < n; i++ {
		copy(s.grid[top:bottom], s.grid[top+1:bottom+1])
		s.grid[bottom] = newRow(s.Cols, Attr{BG: s.Pen.BG})
	}
}

func (s *Screen) scrollDown(top, bottom, n int) {
	s.Scrolled = true
	n = min(n, bottom-top+1)
	for i := 0; i < n; i++ {
		copy(s.grid[top+1:bottom+1], s.grid[top:bottom])
		s.grid[top] = newRow(s.Cols, Attr{BG: s.Pen.BG})
	}
}

func (s *Screen) insertBlank(n int) {
	row := s.grid[s.Row]
	n = min(n, s.Cols-s.Col)
	copy(row[s.Col+n:], row[s.Col:s.Cols-n])
	for i := s.Col; i < s.Col+n; i++ {
		row[i] = blankCell(s.Pen)
	}
}

func (s *Screen) saveCursor() {
	saved := savedCursor{valid: true, row: s.Row, col: s.Col, pen: s.Pen, link: s.PenLink, origin: s.OriginMode, wrapPending: s.PendingWrap, g0Graphics: s.G0Graphics, g1Graphic: s.G1Graphics, shiftOut: s.ShiftOut}
	if s.AltActive {
		s.savedAlt = saved
	} else {
		s.savedMain = saved
	}
}

func (s *Screen) restoreCursor() {
	saved := s.savedMain
	if s.AltActive {
		saved = s.savedAlt
	}
	if !saved.valid {
		s.Row, s.Col, s.Pen, s.PenLink, s.OriginMode, s.PendingWrap = 0, 0, Attr{}, "", false, false
		return
	}
	s.Row, s.Col = min(saved.row, s.Rows-1), min(saved.col, s.Cols-1)
	s.Pen, s.PenLink, s.OriginMode, s.PendingWrap = saved.pen, saved.link, saved.origin, saved.wrapPending
	s.G0Graphics, s.G1Graphics, s.ShiftOut = saved.g0Graphics, saved.g1Graphic, saved.shiftOut
}

func (s *Screen) intParams() ([]int, bool) {
	private := len(s.params) > 0 && (s.params[0] == '?' || s.params[0] == '>' || s.params[0] == '<' || s.params[0] == '=')
	raw := string(s.params)
	if private {
		raw = raw[1:]
	}
	if raw == "" {
		return nil, private
	}
	parts := strings.Split(raw, ";")
	out := make([]int, len(parts))
	for i, p := range parts {
		if j := strings.IndexByte(p, ':'); j >= 0 {
			p = p[:j]
		}
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out, private
}

func param(ps []int, i, def int) int {
	if i < len(ps) && ps[i] > 0 {
		return ps[i]
	}
	return def
}

func (s *Screen) moveTo(row, col int) {
	s.PendingWrap = false
	if s.OriginMode {
		row += s.Top
		s.Row = max(s.Top, min(row, s.Bottom))
	} else {
		s.Row = max(0, min(row, s.Rows-1))
	}
	s.Col = max(0, min(col, s.Cols-1))
}

func (s *Screen) csi(final byte) {
	ps, private := s.intParams()
	leader := byte(0)
	if len(s.params) > 0 && private {
		leader = s.params[0]
	}
	if len(s.inter) > 0 {
		return // DECSCUSR, DECSTR and similar: no screen effect we model
	}
	if leader == '>' || leader == '<' || leader == '=' {
		return
	}
	switch final {
	case '@':
		s.PendingWrap = false
		s.insertBlank(param(ps, 0, 1))
	case 'A':
		s.PendingWrap = false
		top := 0
		if s.Row >= s.Top {
			top = s.Top
		}
		s.Row = max(top, s.Row-param(ps, 0, 1))
	case 'B', 'e':
		s.PendingWrap = false
		bottom := s.Rows - 1
		if s.Row <= s.Bottom {
			bottom = s.Bottom
		}
		s.Row = min(bottom, s.Row+param(ps, 0, 1))
	case 'C', 'a':
		s.PendingWrap = false
		s.Col = min(s.Cols-1, s.Col+param(ps, 0, 1))
	case 'D':
		s.PendingWrap = false
		s.Col = max(0, s.Col-param(ps, 0, 1))
	case 'E':
		s.PendingWrap = false
		s.Row = min(s.Rows-1, s.Row+param(ps, 0, 1))
		s.Col = 0
	case 'F':
		s.PendingWrap = false
		s.Row = max(0, s.Row-param(ps, 0, 1))
		s.Col = 0
	case 'G', '`':
		s.PendingWrap = false
		s.Col = max(0, min(s.Cols-1, param(ps, 0, 1)-1))
	case 'H', 'f':
		s.moveTo(param(ps, 0, 1)-1, param(ps, 1, 1)-1)
	case 'I':
		s.PendingWrap = false
		for i := 0; i < param(ps, 0, 1); i++ {
			s.Col = min(s.Cols-1, (s.Col/8+1)*8)
		}
	case 'Z':
		s.PendingWrap = false
		for i := 0; i < param(ps, 0, 1); i++ {
			s.Col = max(0, (s.Col-1)/8*8)
		}
	case 'd':
		s.PendingWrap = false
		row := param(ps, 0, 1) - 1
		if s.OriginMode {
			row += s.Top
		}
		s.Row = max(0, min(s.Rows-1, row))
	case 'J':
		s.eraseDisplay(param(ps, 0, 0))
	case 'K':
		s.eraseLine(param(ps, 0, 0))
	case 'L':
		if s.Row >= s.Top && s.Row <= s.Bottom {
			s.PendingWrap = false
			s.scrollDown(s.Row, s.Bottom, param(ps, 0, 1))
			s.Col = 0
		}
	case 'M':
		if s.Row >= s.Top && s.Row <= s.Bottom {
			s.PendingWrap = false
			s.scrollUp(s.Row, s.Bottom, param(ps, 0, 1))
			s.Col = 0
		}
	case 'P':
		s.PendingWrap = false
		row := s.grid[s.Row]
		n := min(param(ps, 0, 1), s.Cols-s.Col)
		copy(row[s.Col:], row[s.Col+n:])
		for i := s.Cols - n; i < s.Cols; i++ {
			row[i] = blankCell(s.Pen)
		}
	case 'S':
		if !private {
			s.scrollUp(s.Top, s.Bottom, param(ps, 0, 1))
		}
	case 'T':
		if !private && len(ps) <= 1 {
			s.scrollDown(s.Top, s.Bottom, param(ps, 0, 1))
		}
	case 'X':
		s.PendingWrap = false
		for i := s.Col; i < min(s.Cols, s.Col+param(ps, 0, 1)); i++ {
			s.clearWideAt(s.Row, i)
			s.grid[s.Row][i] = blankCell(s.Pen)
		}
	case 'b':
		if s.lastCh != "" {
			for i := 0; i < param(ps, 0, 1); i++ {
				r, _ := utf8.DecodeRuneInString(s.lastCh)
				s.print(r)
			}
		}
	case 'm':
		if !private {
			s.sgr()
		}
	case 'r':
		if !private {
			top, bottom := param(ps, 0, 1)-1, param(ps, 1, s.Rows)-1
			if top < bottom && bottom < s.Rows {
				s.Top, s.Bottom = top, bottom
			} else {
				s.Top, s.Bottom = 0, s.Rows-1
			}
			s.moveTo(0, 0)
		}
	case 's':
		if !private {
			s.saveCursor()
		}
	case 'u':
		if !private {
			s.restoreCursor()
		}
	case 'h', 'l':
		set := final == 'h'
		for _, p := range ps {
			if private {
				s.privateMode(p, set)
			} else if p == 4 {
				s.InsertMode = set
			}
		}
	}
}

func (s *Screen) privateMode(p int, set bool) {
	switch p {
	case 25:
		s.CursorVisible = set
	case 6:
		s.OriginMode = set
		s.moveTo(0, 0)
	case 7:
		s.AutoWrap = set
		if !set {
			s.PendingWrap = false
		}
	case 2026:
		s.Sync = set
	case 47, 1047, 1049:
		if set == s.AltActive {
			return
		}
		if set {
			if p == 1049 {
				s.saveCursor()
			}
			s.AltActive = true
			s.grid = s.alt
			if p != 47 {
				for r := range s.alt {
					s.alt[r] = newRow(s.Cols, Attr{})
				}
			}
		} else {
			s.AltActive = false
			s.grid = s.main
			if p == 1049 {
				s.restoreCursor()
			}
		}
		s.Scrolled = true
	}
}

func (s *Screen) eraseDisplay(mode int) {
	s.PendingWrap = false
	switch mode {
	case 0:
		s.eraseLine(0)
		for r := s.Row + 1; r < s.Rows; r++ {
			s.grid[r] = newRow(s.Cols, Attr{BG: s.Pen.BG})
		}
	case 1:
		s.eraseLine(1)
		for r := 0; r < s.Row; r++ {
			s.grid[r] = newRow(s.Cols, Attr{BG: s.Pen.BG})
		}
	case 2, 3:
		for r := range s.grid {
			s.grid[r] = newRow(s.Cols, Attr{BG: s.Pen.BG})
		}
		s.Scrolled = true
	}
}

func (s *Screen) eraseLine(mode int) {
	s.PendingWrap = false
	from, to := s.Col, s.Cols
	switch mode {
	case 1:
		from, to = 0, s.Col+1
	case 2:
		from, to = 0, s.Cols
	}
	if from < s.Cols {
		s.clearWideAt(s.Row, from)
	}
	if to-1 >= 0 && to-1 < s.Cols {
		s.clearWideAt(s.Row, to-1)
	}
	for c := from; c < min(to, s.Cols); c++ {
		s.grid[s.Row][c] = blankCell(s.Pen)
	}
}

func (s *Screen) sgr() {
	raw := string(s.params)
	if raw == "" {
		s.Pen = Attr{}
		return
	}
	parts := strings.Split(raw, ";")
	for i := 0; i < len(parts); i++ {
		sub := strings.Split(parts[i], ":")
		n, _ := strconv.Atoi(sub[0])
		switch {
		case n == 0:
			s.Pen = Attr{}
		case n == 1:
			s.Pen.Flags |= AttrBold
		case n == 2:
			s.Pen.Flags |= AttrDim
		case n == 3:
			s.Pen.Flags |= AttrItalic
		case n == 4:
			if len(sub) > 1 && sub[1] == "0" {
				s.Pen.Flags &^= AttrUnderline
			} else {
				s.Pen.Flags |= AttrUnderline
			}
		case n == 5 || n == 6:
			s.Pen.Flags |= AttrBlink
		case n == 7:
			s.Pen.Flags |= AttrInverse
		case n == 8:
			s.Pen.Flags |= AttrHidden
		case n == 9:
			s.Pen.Flags |= AttrStrike
		case n == 21:
			s.Pen.Flags |= AttrUnderline
		case n == 22:
			s.Pen.Flags &^= AttrBold | AttrDim
		case n == 23:
			s.Pen.Flags &^= AttrItalic
		case n == 24:
			s.Pen.Flags &^= AttrUnderline
		case n == 25:
			s.Pen.Flags &^= AttrBlink
		case n == 27:
			s.Pen.Flags &^= AttrInverse
		case n == 28:
			s.Pen.Flags &^= AttrHidden
		case n == 29:
			s.Pen.Flags &^= AttrStrike
		case n >= 30 && n <= 37:
			s.Pen.FG = Color{ColorIndexed, uint32(n - 30)}
		case n == 39:
			s.Pen.FG = Color{}
		case n >= 40 && n <= 47:
			s.Pen.BG = Color{ColorIndexed, uint32(n - 40)}
		case n == 49:
			s.Pen.BG = Color{}
		case n >= 90 && n <= 97:
			s.Pen.FG = Color{ColorIndexed, uint32(n - 90 + 8)}
		case n >= 100 && n <= 107:
			s.Pen.BG = Color{ColorIndexed, uint32(n - 100 + 8)}
		case n == 38 || n == 48 || n == 58:
			var color Color
			var ok bool
			if len(sub) > 1 {
				color, ok = extendedColor(sub[1:])
			} else {
				var used int
				color, used, ok = extendedColorParts(parts[i+1:])
				i += used
			}
			if ok && n == 38 {
				s.Pen.FG = color
			} else if ok && n == 48 {
				s.Pen.BG = color
			}
		}
	}
}

func extendedColor(sub []string) (Color, bool) {
	if len(sub) == 0 {
		return Color{}, false
	}
	switch sub[0] {
	case "5":
		if len(sub) >= 2 {
			n, _ := strconv.Atoi(sub[1])
			return Color{ColorIndexed, uint32(n & 0xff)}, true
		}
	case "2":
		vals := sub[1:]
		if len(vals) == 4 { // colon form may include a colorspace id
			vals = vals[1:]
		}
		if len(vals) >= 3 {
			r, _ := strconv.Atoi(vals[0])
			g, _ := strconv.Atoi(vals[1])
			b, _ := strconv.Atoi(vals[2])
			return Color{ColorRGB, uint32(r&0xff)<<16 | uint32(g&0xff)<<8 | uint32(b&0xff)}, true
		}
	}
	return Color{}, false
}

func extendedColorParts(rest []string) (Color, int, bool) {
	if len(rest) == 0 {
		return Color{}, 0, false
	}
	switch rest[0] {
	case "5":
		if len(rest) >= 2 {
			n, _ := strconv.Atoi(rest[1])
			return Color{ColorIndexed, uint32(n & 0xff)}, 2, true
		}
		return Color{}, len(rest), false
	case "2":
		if len(rest) >= 4 {
			r, _ := strconv.Atoi(rest[1])
			g, _ := strconv.Atoi(rest[2])
			b, _ := strconv.Atoi(rest[3])
			return Color{ColorRGB, uint32(r&0xff)<<16 | uint32(g&0xff)<<8 | uint32(b&0xff)}, 4, true
		}
		return Color{}, len(rest), false
	}
	return Color{}, 1, false
}

// endOSC applies a finished OSC string: OSC 8 opens or closes a hyperlink.
func (s *Screen) endOSC() {
	osc, over := s.osc, s.oscOver
	s.osc, s.oscOver = nil, false
	if osc == nil || len(osc) < 2 || osc[0] != '8' || osc[1] != ';' {
		return
	}
	link := string(osc[2:])
	params, uri, ok := strings.Cut(link, ";")
	switch {
	case over || !ok:
		s.PenLink = "" // too long to model, or malformed: dropped
	case uri == "":
		s.PenLink = ""
	default:
		s.PenLink = params + ";" + uri
	}
}
