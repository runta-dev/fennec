//go:build darwin

package desk

import (
	"testing"
	"time"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
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

// TestHungAppBookkeeping drives miss() with a fake clock: misses must last
// 2 s, after a 10 s launch grace, before the app is skipped for 10 s.
func TestHungAppBookkeeping(t *testing.T) {
	now := time.Unix(1000, 0)
	d := &Desk{apps: map[int]*appState{}, now: func() time.Time { return now }}
	st := &appState{firstSeen: now, last: []view.RawWindow{{Title: "Doc", Active: true}}}
	got := d.miss(1, st, now)
	if len(got) != 1 || !got[0].Stale || got[0].Active {
		t.Fatalf("stale copy %+v", got)
	}
	now = now.Add(3 * time.Second)
	d.miss(1, st, now)
	if st.hung {
		t.Fatal("hung inside the launch grace")
	}
	now = now.Add(8 * time.Second)
	d.miss(1, st, now)
	if !st.hung || !st.skipUntil.Equal(now.Add(hungSkip)) {
		t.Fatalf("not hung after grace and 2 s of misses: %+v", st)
	}
}
