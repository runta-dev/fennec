package predict

import (
	"strings"
	"testing"
)

// TestScreenHyperlinks: OSC 8 links follow xterm.js: printed cells carry the
// open link, SGR does not end it, erased cells have none, DECSC/DECRC keep
// it, an empty URI closes it, and a link too long to model is dropped.
func TestScreenHyperlinks(t *testing.T) {
	s := NewScreen(3, 20)
	s.Write([]byte("\x1b[?1049h\x1b]8;id=a;https://x.example/p?q=1\x1b\\ab\x1b[0m\x1b[1mc\x1b]8;;\x07d"))
	want := []string{"id=a;https://x.example/p?q=1", "id=a;https://x.example/p?q=1", "id=a;https://x.example/p?q=1", ""}
	for col, link := range want {
		if got := s.Cell(0, col).Link; got != link {
			t.Fatalf("col %d: link %q, want %q", col, got, link)
		}
	}
	if s.PenLink != "" {
		t.Fatalf("link still open: %q", s.PenLink)
	}

	s.Write([]byte("\x1b]8;;https://y.example\x07\x1b7\x1b]8;;\x07\x1b8e\x1b[2;1Hf\x1b[1;1H\x1b[2X"))
	if got := s.Cell(0, 4).Link; got != ";https://y.example" {
		t.Fatalf("restored cursor lost the link: %q", got)
	}
	if got := s.Cell(1, 0).Link; got != ";https://y.example" {
		t.Fatalf("second row: %q", got)
	}
	if s.Cell(0, 0).Link != "" || s.Cell(0, 1).Link != "" {
		t.Fatal("erased cells kept their link")
	}

	// An ESC inside the URI that is not ST stays part of it.
	s.Write([]byte("\x1b]8;;https://z.example/\x1bq\x07g"))
	if got := s.PenLink; got != ";https://z.example/\x1bq" {
		t.Fatalf("link with ESC: %q", got)
	}

	long := "\x1b]8;;https://l.example/" + strings.Repeat("x", MaxLinkBytes) + "\x07h"
	s.Write([]byte(long))
	if s.PenLink != "" || s.Cell(1, 2).Link != "" {
		t.Fatalf("over-long link modelled: pen %d bytes", len(s.PenLink))
	}

	// Other strings are consumed without effect.
	s.Write([]byte("\x1b]0;title\x07\x1bP+q\x1b\\i"))
	if s.PenLink != "" {
		t.Fatal("a title or DCS changed the link")
	}
}

// TestDiffScreenHyperlinks: a difference reproduces linked cells, cells whose
// link alone changed, and the open link of the target.
func TestDiffScreenHyperlinks(t *testing.T) {
	from := NewScreen(4, 30)
	from.Write([]byte("\x1b[?1049h\x1b[1;1Hsee \x1b]8;;https://a.example\x07docs\x1b]8;;\x07 now"))
	to := from.Clone()
	to.Write([]byte("\x1b[1;5H\x1b]8;id=2;https://b.example\x1b\\docs\x1b]8;;\x1b\\\x1b[3;1H\x1b]8;;https://c.example\x07open"))
	diff, ok := DiffScreen(from, to, false)
	if !ok {
		t.Fatal("not diffable")
	}
	check := from.Clone()
	check.Write(diff)
	if same, why := SameScreen(check, to); !same {
		t.Fatal(why)
	}
	if !strings.Contains(string(diff), OSC8("id=2;https://b.example")+"docs") || strings.Contains(string(diff), "a.example") {
		t.Fatalf("diff %q", diff)
	}

	// The target's open link differs from the last cell written.
	to2 := to.Clone()
	to2.Write([]byte("\x1b]8;;\x07\x1b[4;1H"))
	diff, ok = DiffScreen(to, to2, false)
	if !ok || !strings.Contains(string(diff), OSC8("")) {
		t.Fatalf("closing the open link: %q", diff)
	}
	check = to.Clone()
	check.Write(diff)
	if same, why := SameScreen(check, to2); !same {
		t.Fatal(why)
	}
}
