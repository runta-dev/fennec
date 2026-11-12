package record

import "unicode"

// Event flag bits of the modifier keys (CGEventFlags).
const (
	flagShift = 1 << 17
	flagCtrl  = 1 << 18
	flagAlt   = 1 << 19
	flagCmd   = 1 << 20
)

// modsOf names the modifiers held, in the key-name vocabulary's order.
func modsOf(flags uint64) []string {
	var out []string
	for _, m := range []struct {
		bit  uint64
		name string
	}{{flagCmd, "cmd"}, {flagCtrl, "ctrl"}, {flagAlt, "alt"}, {flagShift, "shift"}} {
		if flags&m.bit != 0 {
			out = append(out, m.name)
		}
	}
	return out
}

// keyRun tells the first key of each typing run, whose focused field the
// tap reads: a run starts after a click and after any key that is not
// plain text (a control character, or a key with cmd or ctrl).
type keyRun struct{ typing bool }

func (k *keyRun) click() { k.typing = false }

// key reports whether this key starts a run.
func (k *keyRun) key(text string, flags uint64) (first bool) {
	first = !k.typing
	plain := text != "" && flags&(flagCmd|flagCtrl) == 0
	for _, r := range text {
		if unicode.IsControl(r) {
			plain = false
		}
	}
	k.typing = plain
	return first
}

// secureWatch tells when a password field takes the keyboard focus: macOS
// then turns secure input on and hides the keys that follow. Secure input
// already on when the recording starts is no password event.
type secureWatch struct{ on bool }

func (w *secureWatch) turn(on bool) (began bool) {
	began = on && !w.on
	w.on = on
	return began
}
