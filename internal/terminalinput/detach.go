package terminalinput

import (
	"bytes"
	"strconv"
	"strings"
)

// Decoder handles both traditional control bytes and Kitty/xterm keyboard
// protocols. Escape sequences may arrive across multiple transport reads.
type Decoder struct {
	pending []byte
	pasted  bool
}

func (d *Decoder) Feed(data []byte) ([]byte, bool) {
	data = append(d.pending, data...)
	d.pending = nil
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if !d.pasted && (data[i] == 3 || data[i] == 4 || data[i] == 28) {
			return append(out, 28), true
		}
		if data[i] != 27 {
			out = append(out, data[i])
			i++
			continue
		}
		if i+1 == len(data) {
			d.pending = append([]byte(nil), data[i:]...)
			break
		}
		if data[i+1] != '[' {
			out = append(out, data[i])
			i++
			continue
		}
		end := i + 2
		for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
			end++
		}
		if end == len(data) {
			if end-i <= 64 {
				d.pending = append([]byte(nil), data[i:]...)
				break
			}
			out = append(out, data[i])
			i++
			continue
		}
		sequence := data[i : end+1]
		if bytes.Equal(sequence, []byte("\x1b[200~")) {
			d.pasted = true
		} else if bytes.Equal(sequence, []byte("\x1b[201~")) {
			d.pasted = false
		} else if !d.pasted && detachSequence(string(sequence[2:])) {
			return append(out, 28), true
		}
		out = append(out, sequence...)
		i = end + 1
	}
	return out, false
}
func (d *Decoder) Flush() []byte { data := d.pending; d.pending = nil; return data }
func detachSequence(sequence string) bool {
	if len(sequence) < 2 {
		return false
	}
	final := sequence[len(sequence)-1]
	parts := strings.Split(sequence[:len(sequence)-1], ";")
	number := func(s string) int { n, _ := strconv.Atoi(strings.Split(s, ":")[0]); return n }
	key, mod := 0, 1
	switch final {
	case 'u':
		key = number(parts[0])
		if len(parts) > 1 {
			mod = number(parts[1])
			event := strings.Split(parts[1], ":")
			if len(event) > 1 && event[1] == "3" {
				return false
			}
		}
	case '~':
		if len(parts) != 3 || parts[0] != "27" {
			return false
		}
		mod = number(parts[1])
		key = number(parts[2])
	default:
		return false
	}
	if key == 3 || key == 4 || key == 28 {
		return true
	}
	return mod > 0 && (mod-1)&4 != 0 && (key == 99 || key == 67 || key == 100 || key == 68 || key == 92)
}
func Detach(data []byte) ([]byte, bool) {
	d := Decoder{}
	out, detach := d.Feed(data)
	if !detach {
		out = append(out, d.Flush()...)
	}
	return out, detach
}
