package terminalstream

import (
	"strings"
	"testing"

	"fennec/internal/predict"
)

// codexIdle is the frame Codex repeats after a wheel step (recorded through
// herdr-core attach).
const codexIdle = "\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[27;3H\x1b[?25h\x1b[?2026l"

func TestNoopFramesDropped(t *testing.T) {
	scroll := "\x1b[?2026h\x1b[1;1H\x1b[38;5;3mtranscript line\x1b[0m\x1b[27;3H\x1b[?2026l"
	var f NoopFrameFilter
	in := scroll + strings.Repeat(codexIdle, 95)
	out, dropped := f.Filter([]byte(in))
	if want := scroll + codexIdle; string(out) != want {
		t.Fatalf("out %q", out)
	}
	if dropped != 94*len(codexIdle) {
		t.Fatalf("dropped %d", dropped)
	}
	// The repeat continues in the next read.
	out, dropped = f.Filter([]byte(codexIdle + codexIdle))
	if len(out) != 0 || dropped != 2*len(codexIdle) {
		t.Fatalf("next read: out %q dropped %d", out, dropped)
	}
}

func TestNoopFramesKept(t *testing.T) {
	other := "\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[12;3H\x1b[?25h\x1b[?2026l"
	text := "\x1b[?2026h\x1b[27;3Hx\x1b[?2026l"
	for name, in := range map[string]string{
		"different cursor": codexIdle + other + codexIdle,
		"bytes between":    codexIdle + "a" + codexIdle,
		"frame with text":  text + text,
		"other sequence":   "\x1b[?2026h\x1b[2J\x1b[?2026l\x1b[?2026h\x1b[2J\x1b[?2026l",
	} {
		var f NoopFrameFilter
		out, dropped := f.Filter([]byte(in))
		if string(out) != in || dropped != 0 {
			t.Errorf("%s: out %q dropped %d", name, out, dropped)
		}
	}
	// An unfinished frame is held until the next read or Flush.
	var held NoopFrameFilter
	if out, _ := held.Filter([]byte(codexIdle + "\x1b[?2026h\x1b[0m")); string(out) != codexIdle {
		t.Fatalf("split frame: %q", out)
	}
	if rest := held.Flush(); string(rest) != "\x1b[?2026h\x1b[0m" {
		t.Fatalf("flushed %q", rest)
	}
	// Output between reads breaks a repeat.
	var f NoopFrameFilter
	f.Filter([]byte(codexIdle))
	f.Filter([]byte("z"))
	if out, _ := f.Filter([]byte(codexIdle)); string(out) != codexIdle {
		t.Fatalf("repeat after other output dropped")
	}
}

// TestNoopFramesSameScreen: the filtered stream leaves the terminal exactly
// as the original, read by read.
func TestNoopFramesSameScreen(t *testing.T) {
	reads := []string{
		"\x1b[?1049h\x1b[2J\x1b[1;1Hhello",
		"\x1b[?2026h\x1b[1;1H\x1b[31mred\x1b[0m\x1b[5;7H\x1b[?25h\x1b[?2026l" + strings.Repeat("\x1b[?2026h\x1b[31m\x1b[5;7H\x1b[?25l\x1b[?2026l", 40),
		strings.Repeat("\x1b[?2026h\x1b[31m\x1b[5;7H\x1b[?25l\x1b[?2026l", 10) + "after",
		strings.Repeat(codexIdle, 30),
		"\x1b[?2026h\x1b[0m\x1b[3;3Hz\x1b[?2026l" + codexIdle + codexIdle,
	}
	a, b := predict.NewScreen(30, 100), predict.NewScreen(30, 100)
	var f NoopFrameFilter
	var total, kept int
	for _, r := range reads {
		a.Write([]byte(r))
		out, _ := f.Filter([]byte(r))
		b.Write(out)
		total += len(r)
		kept += len(out)
		if predict.DumpScreen(a) != predict.DumpScreen(b) || a.Row != b.Row || a.Col != b.Col || a.CursorVisible != b.CursorVisible {
			t.Fatalf("screens differ after %q", r)
		}
		for row := 0; row < a.Rows; row++ {
			for col := 0; col < a.Cols; col++ {
				if a.Cell(row, col) != b.Cell(row, col) {
					t.Fatalf("cell %d,%d differs", row, col)
				}
			}
		}
	}
	if kept*2 > total {
		t.Fatalf("kept %d of %d bytes", kept, total)
	}
}

// TestNoopFramesSplitReads: Herdr's writes do not follow frame boundaries.
// Split anywhere, the filtered stream (with Flush) leaves the same screen,
// and repeated no-op frames are still dropped.
func TestNoopFramesSplitReads(t *testing.T) {
	scroll := "\x1b[?2026h\x1b[2;1H\x1b[38;5;3mscrolled line\x1b[0m\x1b[27;3H\x1b[?25h\x1b[?2026l"
	stream := "\x1b[?1049h\x1b[2J" + scroll + strings.Repeat(codexIdle, 60) + "\x1b[5;5Hplain" + strings.Repeat(codexIdle, 20)
	want := predict.NewScreen(30, 100)
	want.Write([]byte(stream))
	for _, size := range []int{1, 2, 3, 5, 7, 13, 29, 43, 86, 338} {
		var f NoopFrameFilter
		got := predict.NewScreen(30, 100)
		kept := 0
		for i := 0; i < len(stream); i += size {
			out, _ := f.Filter([]byte(stream[i:min(len(stream), i+size)]))
			got.Write(out)
			kept += len(out)
		}
		rest := f.Flush()
		got.Write(rest)
		kept += len(rest)
		if predict.DumpScreen(got) != predict.DumpScreen(want) || got.Row != want.Row || got.Col != want.Col || got.CursorVisible != want.CursorVisible {
			t.Fatalf("reads of %d bytes: screen differs", size)
		}
		for row := 0; row < want.Rows; row++ {
			for col := 0; col < want.Cols; col++ {
				if got.Cell(row, col) != want.Cell(row, col) {
					t.Fatalf("reads of %d bytes: cell %d,%d differs", size, row, col)
				}
			}
		}
		if limit := len(stream) - 70*len(codexIdle); kept > limit {
			t.Errorf("reads of %d bytes: kept %d bytes, want at most %d", size, kept, limit)
		}
	}
}

// TestNoopFramesHoldOnlyControls: an unfinished frame is held only while it
// could still be a no-op; text inside it passes at once.
func TestNoopFramesHoldOnlyControls(t *testing.T) {
	var f NoopFrameFilter
	if out, _ := f.Filter([]byte("\x1b[?2026h\x1b[39m\x1b[2")); len(out) != 0 {
		t.Fatalf("emitted %q", out)
	}
	if out, _ := f.Filter([]byte("7;3H\x1b[?25h\x1b[?2026l")); string(out) != "\x1b[?2026h\x1b[39m\x1b[27;3H\x1b[?25h\x1b[?2026l" {
		t.Fatalf("completed frame %q", out)
	}
	if out, _ := f.Filter([]byte("\x1b[?2026h\x1b[1;1Htext")); string(out) != "\x1b[?2026h\x1b[1;1Htext" {
		t.Fatalf("text held: %q", out)
	}
	if rest := f.Flush(); len(rest) != 0 {
		t.Fatalf("held %q", rest)
	}
}
