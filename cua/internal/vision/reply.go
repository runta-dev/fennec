package vision

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ScreenshotPoints restates every numeric pair "(x,y)" or "(x, y)" of a
// driver reply in screenshot pixels: divided by scale and rounded.
// Parenthesised text that is not a numeric pair is left as it is.
func ScreenshotPoints(text string, scale float64) string {
	if scale == 1 || !Positive(scale) {
		return text
	}
	var b strings.Builder
	rest := text
	for {
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			break
		}
		b.WriteString(rest[:open])
		rest = rest[open:]
		end := strings.IndexByte(rest, ')')
		x, y, ok := pair(rest, end)
		if !ok {
			b.WriteByte('(')
			rest = rest[1:]
			continue
		}
		fmt.Fprintf(&b, "(%.0f,%.0f)", math.Round(x/scale), math.Round(y/scale))
		rest = rest[end+1:]
	}
	b.WriteString(rest)
	return b.String()
}

func pair(s string, end int) (float64, float64, bool) {
	if end < 0 {
		return 0, 0, false
	}
	a, c, found := strings.Cut(s[1:end], ",")
	if !found {
		return 0, 0, false
	}
	x, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	y, err2 := strconv.ParseFloat(strings.TrimSpace(c), 64)
	return x, y, err1 == nil && err2 == nil
}

// ClickReply words a driver's desktop click reply for the agent: the click
// is said to land at the screenshot point the agent asked for, not at the
// screen-absolute or device-pixel coordinates the driver used, and the
// reply's remaining qualifiers are kept. Other points are restated by
// ScreenshotPoints.
func ClickReply(text string, asked [2]float64, scale float64) string {
	head, rest, found := strings.Cut(text, "Sent screen-absolute ")
	if !found {
		return ScreenshotPoints(text, scale)
	}
	click, rest, found := strings.Cut(rest, " at ")
	if !found {
		return ScreenshotPoints(text, scale)
	}
	// rest: "desktop-pixel (a,b) → screen-point (c,d) (qualifiers)." or "(a,b) (qualifiers)."
	if r, ok := strings.CutPrefix(rest, "desktop-pixel "); ok {
		_, after, found := strings.Cut(r, " → screen-point ")
		if !found {
			return ScreenshotPoints(text, scale)
		}
		rest = after
	}
	end := strings.IndexByte(rest, ')')
	if _, _, ok := pair(rest, end); !ok || !strings.HasPrefix(rest, "(") {
		return ScreenshotPoints(text, scale)
	}
	return ScreenshotPoints(head, scale) + fmt.Sprintf("Sent %s at screenshot point (%s,%s)", click, num(asked[0]), num(asked[1])) + ScreenshotPoints(rest[end+1:], scale)
}
