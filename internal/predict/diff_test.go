package predict

import (
	"math/rand"
	"strings"
	"testing"
)

// randomFrame writes a frame of the kind agents draw: cursor moves, colours,
// text (wide characters included), erases and scrolls of a region.
func randomFrame(rng *rand.Rand, rows, cols int) string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	words := []string{"hello", "world", "\u4f60\u597d", "x", "  ", "line", "\u2500\u2500\u2500", "a\u0301"}
	for i := 0; i < 1+rng.Intn(12); i++ {
		switch rng.Intn(9) {
		case 0, 1, 2:
			b.WriteString("\x1b[" + itoa(1+rng.Intn(rows)) + ";" + itoa(1+rng.Intn(cols)) + "H")
		case 3:
			b.WriteString([]string{"\x1b[0m", "\x1b[1;31m", "\x1b[38;5;214m", "\x1b[48;2;10;20;30m", "\x1b[7m", "\x1b[22;39m"}[rng.Intn(6)])
		case 4, 5:
			b.WriteString(words[rng.Intn(len(words))])
		case 6:
			b.WriteString([]string{"\x1b[K", "\x1b[1K", "\x1b[2K", "\x1b[J", "\x1b[3X"}[rng.Intn(5)])
		case 7:
			top := 1 + rng.Intn(rows/2)
			b.WriteString("\x1b[" + itoa(top) + ";" + itoa(top+1+rng.Intn(rows-top)) + "r\x1b[" + itoa(1+rng.Intn(2)) + []string{"S", "T", "L", "M"}[rng.Intn(4)])
		case 8:
			b.WriteString([]string{"\r\n", "\x1b[?25l", "\x1b[?25h", "\x1b[2C", "\x1b[A"}[rng.Intn(5)])
		}
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

func itoa(n int) string {
	s := ""
	for {
		s = string(rune('0'+n%10)) + s
		n /= 10
		if n == 0 {
			return s
		}
	}
}

// TestDiffScreenRandom: for random runs of frames, the diff from any earlier
// state to the latest reproduces it exactly (DiffScreen checks itself; here
// the result is also replayed independently).
func TestDiffScreenRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	ok, skipped := 0, 0
	for trial := 0; trial < 400; trial++ {
		rows, cols := 6+rng.Intn(20), 20+rng.Intn(80)
		from := NewScreen(rows, cols)
		from.Write([]byte("\x1b[?1049h\x1b[2J"))
		for i := 0; i < rng.Intn(5); i++ {
			from.Write([]byte(randomFrame(rng, rows, cols)))
		}
		to := from.Clone()
		for i := 0; i < 1+rng.Intn(8); i++ {
			to.Write([]byte(randomFrame(rng, rows, cols)))
		}
		for _, full := range []bool{false, true} {
			diff, done := DiffScreen(from, to, full)
			if !done {
				if !to.PendingWrap {
					t.Fatalf("trial %d: diff refused without a reason", trial)
				}
				skipped++
				continue
			}
			replay := from.Clone()
			replay.Write(diff)
			if same, why := SameScreen(replay, to); !same {
				t.Fatalf("trial %d full=%v: %s", trial, full, why)
			}
			ok++
		}
	}
	t.Logf("%d diffs checked, %d refused (pending wrap)", ok, skipped)
	if ok < 600 {
		t.Fatalf("only %d diffs", ok)
	}
}

// TestDiffScreenRefuses what it cannot reproduce.
func TestDiffScreenRefuses(t *testing.T) {
	main := NewScreen(5, 20)
	if _, ok := DiffScreen(main, main.Clone(), false); ok {
		t.Fatal("main screen diffed")
	}
	alt := NewScreen(5, 20)
	alt.Write([]byte("\x1b[?1049h"))
	half := alt.Clone()
	half.Write([]byte("\x1b[?2026hpartial"))
	if _, ok := DiffScreen(alt, half, false); ok {
		t.Fatal("open synchronized frame diffed")
	}
	graphics := alt.Clone()
	graphics.Write([]byte("\x1b(0"))
	if _, ok := DiffScreen(alt, graphics, false); ok {
		t.Fatal("line-drawing charset diffed")
	}
}
