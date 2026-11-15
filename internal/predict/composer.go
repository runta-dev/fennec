package predict

import (
	"regexp"
	"strconv"
	"strings"
)

// Helpers shared by composer profiles.

// rowCells renders elements of one visual row as cells starting at col, with
// tokens styled by tokenAttr.
func rowCells(dst []Cell, col int, elems []Elem, textAttr, tokenAttr Attr) int {
	for _, e := range elems {
		if e.S == "\n" {
			continue
		}
		attr := textAttr
		if e.Token {
			attr = tokenAttr
		}
		for _, r := range e.S {
			w := RuneWidth(r)
			if w == 0 {
				if col > 0 && col-1 < len(dst) {
					dst[col-1].Text += string(r)
				}
				continue
			}
			if col+w > len(dst) {
				return col
			}
			dst[col] = Cell{Text: string(r), Width: uint8(w), Attr: attr}
			if w == 2 {
				dst[col+1] = Cell{Width: 0, Attr: attr}
			}
			col += w
		}
	}
	return col
}

func blankRow(cols int, attr Attr) []Cell {
	row := make([]Cell, cols)
	for i := range row {
		row[i] = Cell{Width: 1, Attr: Attr{BG: attr.BG}}
	}
	return row
}

func copyRow(s *Screen, r int) []Cell {
	if r < 0 || r >= s.Rows {
		return nil
	}
	return append([]Cell(nil), s.grid[r]...)
}

// rowBlank reports whether a screen row shows nothing (spaces only).
func rowBlank(s *Screen, r int, from, to int) bool {
	for c := max(0, from); c < min(to, s.Cols); c++ {
		cell := s.Cell(r, c)
		if cell.Width != 0 && !cell.Blank() {
			return false
		}
	}
	return true
}

// rowRun reads the cells of a row as text and the display width of its
// non-blank prefix (trailing blanks trimmed).
func rowRun(s *Screen, r, from, to int) (string, int) {
	var b strings.Builder
	width, trimmed, trimmedWidth := 0, 0, 0
	for c := from; c < min(to, s.Cols); c++ {
		cell := s.Cell(r, c)
		if cell.Width == 0 {
			continue
		}
		b.WriteString(cell.Glyph())
		width += int(cell.Width)
		if !cell.Blank() {
			trimmed, trimmedWidth = b.Len(), width
		}
	}
	return b.String()[:trimmed], trimmedWidth
}

// renderRows lays out st with wrap and compares row text with the screen.
func layoutStrings(elems []Elem, rows []VisualRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		var b strings.Builder
		for _, e := range elems[r.Start+r.Hidden : r.End] {
			if e.S != "\n" {
				b.WriteString(e.S)
			}
		}
		out[i] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// tokenize turns token text matched by re into atomic elements.
func tokenize(text string, re *regexp.Regexp) []Elem {
	if re == nil {
		return TextElems(text)
	}
	var out []Elem
	last := 0
	for _, m := range re.FindAllStringIndex(text, -1) {
		out = append(out, TextElems(text[last:m[0]])...)
		out = append(out, Elem{S: text[m[0]:m[1]], Token: true})
		last = m[1]
	}
	return append(out, TextElems(text[last:])...)
}

// maxTokenNumber returns the highest "#N" in matching tokens.
func maxTokenNumber(elems []Elem, re *regexp.Regexp) int {
	n := 0
	for _, e := range elems {
		if !e.Token {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(e.S, -1) {
			if len(m) > 1 {
				if v, err := strconv.Atoi(m[1]); err == nil && v > n {
					n = v
				}
			}
		}
	}
	return n
}

// printable reports whether every rune of text can be predicted.
func printable(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r != ' ' && !PredictableRune(r) {
			return false
		}
	}
	return true
}

func countNewlines(s string) int { return strings.Count(s, "\n") }

func normalizePaste(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

var imagePathPattern = regexp.MustCompile(`(?i)^\s*['"]?(/|~/|file://)[^\n]*\.(png|jpe?g|gif|webp|bmp|tiff?|heic)['"]?\s*$`)

// looksLikeImagePath reports pastes the agent may turn into an attachment
// depending on the remote filesystem, which the client cannot see.
func looksLikeImagePath(s string) bool { return imagePathPattern.MatchString(s) }

// rowString is the visible text of a row of cells.
func rowString(row []Cell) string {
	var b strings.Builder
	for _, c := range row {
		if c.Width != 0 {
			b.WriteString(c.Glyph())
		}
	}
	return b.String()
}
