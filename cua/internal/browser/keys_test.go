package browser

import (
	"strings"
	"testing"
)

func TestPlaywrightKeys(t *testing.T) {
	cases := map[string]string{
		"a": "a", "A": "A", "Return": "Enter", "Tab": "Tab", "Escape": "Escape", "BackSpace": "Backspace",
		"Delete": "Delete", "Up": "ArrowUp", "Down": "ArrowDown", "Left": "ArrowLeft", "Right": "ArrowRight",
		"Page_Down": "PageDown", "Page_Up": "PageUp", "Prior": "PageUp", "Next": "PageDown", "Home": "Home", "End": "End",
		"space": "Space", "KP_0": "Numpad0", "KP_9": "Numpad9", "KP_Add": "NumpadAdd", "KP_Enter": "NumpadEnter",
		"F5": "F5", "f12": "F12", "super+c": "Meta+c", "cmd+shift+z": "Meta+Shift+z", "shift+Tab": "Shift+Tab",
		"ctrl+alt+Delete": "Control+Alt+Delete", "ctrl++": "Control++", "ctrl+plus": "Control++", "alt+minus": "Alt+-",
		"Escape ": "Escape",
	}
	for in, want := range cases {
		keys, err := PlaywrightKeys(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := strings.Join(keys, "+"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "nope", "ctrl+", "fn+a", "Insert", "F13"} {
		if _, err := PlaywrightKeys(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
