package predict

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

// RuneWidth returns the terminal column width of r: 0 for combining and
// zero-width code points, 2 for East Asian Wide and Fullwidth, otherwise 1.
func RuneWidth(r rune) int {
	switch {
	case r == 0x200d || r == 0x200b || r == 0x200c || r == 0x2060 || r == 0xfeff:
		return 0
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// PredictableRune reports whether r renders identically in the agents and in
// common terminals: printable, not ambiguous width, not an emoji presentation
// or joiner. Anything else makes an edit unpredictable.
func PredictableRune(r rune) bool {
	if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
		return false
	}
	if r < 0x7f {
		return true
	}
	if !unicode.IsPrint(r) || RuneWidth(r) == 0 {
		return false
	}
	if width.LookupRune(r).Kind() == width.EastAsianAmbiguous {
		return false
	}
	// Emoji and pictographs: widths disagree between terminals and libraries.
	if r >= 0x1f000 || (r >= 0x2600 && r <= 0x27bf) || (r >= 0x2b00 && r <= 0x2bff) {
		return false
	}
	return true
}

// StringWidth is the column width of s.
func StringWidth(s string) int {
	n := 0
	for _, r := range s {
		n += RuneWidth(r)
	}
	return n
}

// SGR renders an attribute as a complete SGR sequence starting from reset.
func SGR(a Attr) string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	flags := []struct {
		bit  uint16
		code string
	}{{AttrBold, "1"}, {AttrDim, "2"}, {AttrItalic, "3"}, {AttrUnderline, "4"}, {AttrBlink, "5"}, {AttrInverse, "7"}, {AttrHidden, "8"}, {AttrStrike, "9"}}
	for _, f := range flags {
		if a.Flags&f.bit != 0 {
			b.WriteString(";" + f.code)
		}
	}
	writeColor(&b, a.FG, 30, 90, 38)
	writeColor(&b, a.BG, 40, 100, 48)
	b.WriteByte('m')
	return b.String()
}

func writeColor(b *strings.Builder, c Color, base, bright, extended int) {
	switch c.Kind {
	case ColorIndexed:
		switch {
		case c.Value < 8:
			b.WriteString(";" + strconv.Itoa(base+int(c.Value)))
		case c.Value < 16:
			b.WriteString(";" + strconv.Itoa(bright+int(c.Value)-8))
		default:
			b.WriteString(";" + strconv.Itoa(extended) + ";5;" + strconv.Itoa(int(c.Value)))
		}
	case ColorRGB:
		b.WriteString(";" + strconv.Itoa(extended) + ";2;" + strconv.Itoa(int(c.Value>>16&0xff)) + ";" + strconv.Itoa(int(c.Value>>8&0xff)) + ";" + strconv.Itoa(int(c.Value&0xff)))
	}
}
