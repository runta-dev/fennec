//go:build darwin

package desk

import (
	"testing"

	"fennec/cua/internal/ax"
)

func TestShortcutRendering(t *testing.T) {
	cases := []struct {
		char string
		mods any
		want string
	}{
		{"S", 0.0, "cmd+s"},
		{"S", 1.0, "shift+cmd+s"},
		{"Q", 6.0, "ctrl+alt+cmd+q"},
		{"F", 8.0, "f"},
		{" ", nil, "cmd+space"},
		{"", 0.0, ""},
	}
	for _, c := range cases {
		if got := shortcut(c.char, c.mods); got != c.want {
			t.Errorf("%q %v: %q, want %q", c.char, c.mods, got, c.want)
		}
	}
}

func TestValueString(t *testing.T) {
	for in, want := range map[any]string{"abc": "abc", 1.0: "1", 0.5: "0.5", true: "1", false: "0"} {
		if got, ok := valueString(in); !ok || got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
	if _, ok := valueString(ax.Point{}); ok {
		t.Error("a point has a text value")
	}
}

func TestToDeskScalesAndRounds(t *testing.T) {
	got := toDesk(ax.Rect{X: -10.25, Y: 34, W: 100.5, H: 20}, 2)
	if *got != [4]int{-21, 68, 201, 40} {
		t.Errorf("got %v", *got)
	}
	if toDesk(ax.Rect{}, 2) != nil {
		t.Error("empty rect has bounds")
	}
}

// An AppKit text link gives its AXURL as a string (found live: the
// fixture's "Fennec Docs" link carried no URL).
func TestURLOfString(t *testing.T) {
	if urlOf("https://example.com/a") != "https://example.com/a" || urlOf(ax.URL("u")) != "u" || urlOf(3.0) != "" {
		t.Fatal("urlOf")
	}
}
