package terminalstream

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
)

// These are facts supplied by the caller's actual terminal, not guessed colors.
type DisplayFacts struct {
	Foreground string `json:"foreground,omitempty"`
	Background string `json:"background,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	CellWidth  int    `json:"cell_width,omitempty"`
	CellHeight int    `json:"cell_height,omitempty"`
}

const DisplayQuery = "\x1b]10;?\x1b\\\x1b]11;?\x1b\\\x1b]12;?\x1b\\\x1b[16t\x1b[c"

var colorReply = regexp.MustCompile("\x1b\\](10|11|12);rgb:([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})(?:\x07|\x1b\\\\)")
var cellReply = regexp.MustCompile("\x1b\\[6;([0-9]{1,5});([0-9]{1,5})t")
var attributesReply = regexp.MustCompile("\x1b\\[\\?[0-9;]{1,64}c")

type DisplayCollector struct {
	Facts   DisplayFacts
	Done    bool
	pending []byte
}

func (c *DisplayCollector) Feed(data []byte) {
	c.pending = append(c.pending, data...)
	c.pending = colorReply.ReplaceAllFunc(c.pending, func(raw []byte) []byte {
		parts := colorReply.FindSubmatch(raw)
		values := [3]uint64{}
		for i := range values {
			value, _ := strconv.ParseUint(string(parts[i+2]), 16, 16)
			bits := uint(len(parts[i+2]) * 4)
			values[i] = value * 255 / ((1 << bits) - 1)
		}
		color := fmt.Sprintf("#%02x%02x%02x", values[0], values[1], values[2])
		switch string(parts[1]) {
		case "10":
			c.Facts.Foreground = color
		case "11":
			c.Facts.Background = color
		case "12":
			c.Facts.Cursor = color
		}
		return nil
	})
	c.pending = cellReply.ReplaceAllFunc(c.pending, func(raw []byte) []byte {
		parts := cellReply.FindSubmatch(raw)
		height, _ := strconv.Atoi(string(parts[1]))
		width, _ := strconv.Atoi(string(parts[2]))
		if width > 0 && height > 0 && width <= 4096 && height <= 4096 {
			c.Facts.CellWidth = width
			c.Facts.CellHeight = height
		}
		return nil
	})
	c.pending = attributesReply.ReplaceAllFunc(c.pending, func([]byte) []byte { c.Done = true; return nil })
}
func (c *DisplayCollector) Input() []byte { return append([]byte{}, c.pending...) }
func (f DisplayFacts) Replies() []byte {
	var output bytes.Buffer
	color := regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	for _, item := range []struct {
		code  int
		value string
	}{{10, f.Foreground}, {11, f.Background}, {12, f.Cursor}} {
		if color.MatchString(item.value) {
			fmt.Fprintf(&output, "\x1b]%d;rgb:%s/%s/%s\x1b\\", item.code, item.value[1:3], item.value[3:5], item.value[5:7])
		}
	}
	if f.CellWidth > 0 && f.CellWidth <= 4096 && f.CellHeight > 0 && f.CellHeight <= 4096 {
		fmt.Fprintf(&output, "\x1b[6;%d;%dt", f.CellHeight, f.CellWidth)
	}
	// Herdr uses DA1 only as the end marker for its outer-display inquiry.
	output.WriteString("\x1b[?1;2c")
	return output.Bytes()
}

// DisplayResponder consumes only Herdr's initial, exact display inquiry. All
// other bytes remain unchanged, including a nonmatching or truncated prefix.
type DisplayResponder struct {
	Facts   *DisplayFacts
	pending []byte
	done    bool
}

func (r *DisplayResponder) Filter(data []byte) (output, reply []byte, refunded int) {
	if r.done || r.Facts == nil {
		return data, nil, 0
	}
	r.pending = append(r.pending, data...)
	query := []byte(DisplayQuery)
	if len(r.pending) < len(query) && bytes.HasPrefix(query, r.pending) {
		return nil, nil, 0
	}
	r.done = true
	output = r.pending
	r.pending = nil
	if bytes.HasPrefix(output, query) {
		return output[len(query):], r.Facts.Replies(), len(query)
	}
	return output, nil, 0
}
func (r *DisplayResponder) Flush() []byte {
	pending := r.pending
	r.pending = nil
	r.done = true
	return pending
}
