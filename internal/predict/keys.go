package predict

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// KeyKind classifies one decoded input event.
type KeyKind uint8

const (
	KeyUnknown KeyKind = iota
	KeyText            // Text holds printable characters
	KeyPaste           // Text holds bracketed paste contents
	KeyEnter
	KeyNewline // Shift+Enter, Alt+Enter, Ctrl+J and equivalents
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyTab
	KeyEscape
	KeyCtrl          // Ctrl+letter; Rune holds the lowercase letter
	KeyAlt           // Alt+key; Rune holds the key, or KeyBackspace in Base
	KeyImagePaste    // the image paste shortcut
	KeyNeutral       // mouse reports, focus reports, key releases
	KeyWordLeft      // Alt+Left / Alt+B
	KeyWordRight     // Alt+Right / Alt+F
	KeyAltBackspace  // delete previous word
	KeyPasteFragment // part of a bracketed paste split across reads
)

type Key struct {
	Kind KeyKind
	Text string
	Rune rune
	// Open: a bracketed paste started and did not end in this input.
	Open bool
	// Bytes is the number of input bytes the key consumed.
	Bytes int
}

// ParseKeys splits decoded terminal input into keys. Input has already passed
// through terminalinput.Decoder, so escape sequences are complete.
func ParseKeys(data []byte) []Key {
	var keys []Key
	for i := 0; i < len(data); {
		key, n := parseKey(data[i:])
		if n <= 0 {
			n = 1
		}
		key.Bytes = n
		// Merge adjacent printable text into one key.
		if key.Kind == KeyText && len(keys) > 0 && keys[len(keys)-1].Kind == KeyText {
			last := &keys[len(keys)-1]
			last.Text += key.Text
			last.Bytes += n
		} else {
			keys = append(keys, key)
		}
		i += n
	}
	return keys
}

const pasteStart, pasteEnd = "\x1b[200~", "\x1b[201~"

func parseKey(b []byte) (Key, int) {
	s := string(b)
	if strings.HasPrefix(s, pasteStart) {
		end := strings.Index(s, pasteEnd)
		if end < 0 {
			return Key{Kind: KeyPasteFragment, Open: true}, len(s)
		}
		return Key{Kind: KeyPaste, Text: s[len(pasteStart):end]}, end + len(pasteEnd)
	}
	c := b[0]
	switch {
	case c == '\r':
		return Key{Kind: KeyEnter}, 1
	case c == '\n':
		return Key{Kind: KeyNewline}, 1
	case c == 0x7f || c == 0x08:
		return Key{Kind: KeyBackspace}, 1
	case c == '\t':
		return Key{Kind: KeyTab}, 1
	case c == 0x16:
		return Key{Kind: KeyImagePaste}, 1
	case c == 0x1b:
		return parseEscape(b)
	case c < 0x20:
		return Key{Kind: KeyCtrl, Rune: rune(c + 0x60)}, 1
	}
	r, n := utf8.DecodeRune(b)
	if r == utf8.RuneError && n <= 1 {
		return Key{Kind: KeyUnknown}, 1
	}
	return Key{Kind: KeyText, Text: string(r)}, n
}

func parseEscape(b []byte) (Key, int) {
	if len(b) == 1 {
		return Key{Kind: KeyEscape}, 1
	}
	switch b[1] {
	case '\r':
		return Key{Kind: KeyNewline}, 2
	case 0x7f, 0x08:
		return Key{Kind: KeyAltBackspace}, 2
	case 'b':
		return Key{Kind: KeyWordLeft}, 2
	case 'f':
		return Key{Kind: KeyWordRight}, 2
	case 'O':
		if len(b) >= 3 {
			switch b[2] {
			case 'A':
				return Key{Kind: KeyUp}, 3
			case 'B':
				return Key{Kind: KeyDown}, 3
			case 'C':
				return Key{Kind: KeyRight}, 3
			case 'D':
				return Key{Kind: KeyLeft}, 3
			case 'H':
				return Key{Kind: KeyHome}, 3
			case 'F':
				return Key{Kind: KeyEnd}, 3
			}
			return Key{Kind: KeyUnknown}, 3
		}
		return Key{Kind: KeyUnknown}, 2
	case '[':
		end := 2
		for end < len(b) && (b[end] < 0x40 || b[end] > 0x7e) {
			end++
		}
		if end >= len(b) {
			return Key{Kind: KeyUnknown}, len(b)
		}
		return parseCSI(string(b[2:end]), b[end]), end + 1
	}
	r, n := utf8.DecodeRune(b[1:])
	return Key{Kind: KeyAlt, Rune: r}, 1 + n
}

func parseCSI(params string, final byte) Key {
	// SGR and legacy mouse reports, focus reports.
	if strings.HasPrefix(params, "<") && (final == 'M' || final == 'm') {
		return Key{Kind: KeyNeutral}
	}
	if params == "" && (final == 'I' || final == 'O') {
		return Key{Kind: KeyNeutral}
	}
	if params == "" && final == 'M' {
		return Key{Kind: KeyNeutral}
	}
	fields := strings.Split(params, ";")
	mods, event := 1, 1
	if len(fields) > 1 {
		parts := strings.Split(fields[1], ":")
		mods, _ = strconv.Atoi(parts[0])
		if len(parts) > 1 {
			event, _ = strconv.Atoi(parts[1])
		}
	}
	if event == 3 {
		return Key{Kind: KeyNeutral}
	}
	if event == 0 {
		event = 1
	}
	shift, alt, ctrl := false, false, false
	if mods > 1 {
		m := mods - 1
		shift, alt, ctrl = m&1 != 0, m&2 != 0, m&4 != 0
		if m&^7 != 0 { // super, hyper, meta, caps or num lock
			if m&^(7|64|128) != 0 {
				return Key{Kind: KeyUnknown}
			}
		}
	}
	plain := !shift && !alt && !ctrl
	switch final {
	case 'A':
		if plain {
			return Key{Kind: KeyUp}
		}
	case 'B':
		if plain {
			return Key{Kind: KeyDown}
		}
	case 'C':
		if plain {
			return Key{Kind: KeyRight}
		}
		if alt || ctrl {
			return Key{Kind: KeyWordRight}
		}
	case 'D':
		if plain {
			return Key{Kind: KeyLeft}
		}
		if alt || ctrl {
			return Key{Kind: KeyWordLeft}
		}
	case 'H':
		if plain {
			return Key{Kind: KeyHome}
		}
	case 'F':
		if plain {
			return Key{Kind: KeyEnd}
		}
	case '~':
		code, _ := strconv.Atoi(strings.Split(fields[0], ":")[0])
		if code == 27 && len(fields) == 3 { // xterm modifyOtherKeys
			key, _ := strconv.Atoi(fields[2])
			return kittyKey(rune(key), "", shift, alt, ctrl)
		}
		if !plain {
			return Key{Kind: KeyUnknown}
		}
		switch code {
		case 1, 7:
			return Key{Kind: KeyHome}
		case 4, 8:
			return Key{Kind: KeyEnd}
		case 3:
			return Key{Kind: KeyDelete}
		}
	case 'u':
		codes := strings.Split(fields[0], ":")
		code, _ := strconv.Atoi(codes[0])
		shifted := ""
		if shift && len(codes) > 1 && codes[1] != "" {
			if r, err := strconv.Atoi(codes[1]); err == nil {
				shifted = string(rune(r))
			}
		}
		if len(fields) > 2 { // associated text (flag 16)
			var b strings.Builder
			for _, p := range strings.Split(fields[2], ":") {
				if r, err := strconv.Atoi(p); err == nil {
					b.WriteRune(rune(r))
				}
			}
			if !alt && !ctrl && b.Len() > 0 {
				return Key{Kind: KeyText, Text: b.String()}
			}
		}
		return kittyKey(rune(code), shifted, shift, alt, ctrl)
	}
	return Key{Kind: KeyUnknown}
}

func kittyKey(code rune, shifted string, shift, alt, ctrl bool) Key {
	switch code {
	case 13:
		if shift || alt || ctrl {
			return Key{Kind: KeyNewline}
		}
		return Key{Kind: KeyEnter}
	case 9:
		if !shift && !alt && !ctrl {
			return Key{Kind: KeyTab}
		}
		return Key{Kind: KeyUnknown}
	case 27:
		if !shift && !alt && !ctrl {
			return Key{Kind: KeyEscape}
		}
		return Key{Kind: KeyUnknown}
	case 127, 8:
		if alt || ctrl {
			return Key{Kind: KeyAltBackspace}
		}
		if !shift {
			return Key{Kind: KeyBackspace}
		}
		return Key{Kind: KeyUnknown}
	}
	if ctrl && !alt && !shift {
		if code == 'v' || code == 'V' {
			return Key{Kind: KeyImagePaste}
		}
		if code == 'j' {
			return Key{Kind: KeyNewline}
		}
		if code >= 'a' && code <= 'z' {
			return Key{Kind: KeyCtrl, Rune: code}
		}
	}
	if ctrl && shift && !alt && (code == 'v' || code == 'V') {
		return Key{Kind: KeyImagePaste}
	}
	if alt && !ctrl {
		switch code {
		case 'b':
			return Key{Kind: KeyWordLeft}
		case 'f':
			return Key{Kind: KeyWordRight}
		}
		return Key{Kind: KeyAlt, Rune: code}
	}
	if ctrl || alt {
		return Key{Kind: KeyUnknown}
	}
	if code >= 0xe000 && code <= 0xf8ff { // Kitty functional keys
		return Key{Kind: KeyUnknown}
	}
	if shift {
		if shifted != "" {
			return Key{Kind: KeyText, Text: shifted}
		}
		return Key{Kind: KeyUnknown}
	}
	if code >= 0x20 {
		return Key{Kind: KeyText, Text: string(code)}
	}
	return Key{Kind: KeyUnknown}
}

// PasteSplitter carries the state of a bracketed paste across reads: a large
// paste can arrive in several reads, and its middle must not be taken for
// typing. Use one per input stream.
type PasteSplitter struct{ open bool }

// Keys parses data like ParseKeys, continuing an open paste.
func (p *PasteSplitter) Keys(data []byte) []Key {
	var keys []Key
	if p.open {
		end := strings.Index(string(data), pasteEnd)
		if end < 0 {
			return []Key{{Kind: KeyPasteFragment, Bytes: len(data)}}
		}
		n := end + len(pasteEnd)
		keys = append(keys, Key{Kind: KeyPasteFragment, Bytes: n})
		data = data[n:]
		p.open = false
	}
	rest := ParseKeys(data)
	if n := len(rest); n > 0 && rest[n-1].Open {
		p.open = true
	}
	return append(keys, rest...)
}
