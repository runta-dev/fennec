package terminalinput

import "testing"

func TestImagePasteShortcut(t *testing.T) {
	for _, s := range []string{"\x16", "\x1b[118;5u", "\x1b[118;5:1u", "\x1b[118;5:2u", "\x1b[86;6u", "\x1b[27;5;118~"} {
		if !ImagePasteShortcut([]byte(s)) {
			t.Errorf("missed %q", s)
		}
	}
	for _, s := range []string{"v", "\x1bv", "\x1b[118;5:3u", "\x1b[118;3u", "\x1b[118;1u", "\x1b[118u", "\x1b[", "\x1b[200~", "\x16x"} {
		if ImagePasteShortcut([]byte(s)) {
			t.Errorf("intercepted %q", s)
		}
	}
}
