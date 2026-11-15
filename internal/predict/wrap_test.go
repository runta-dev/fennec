package predict

import (
	"reflect"
	"strings"
	"testing"
)

func rowStrings(elems []Elem, rows []VisualRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		var b strings.Builder
		for _, e := range elems[r.Start:r.End] {
			if e.S != "\n" {
				b.WriteString(e.S)
			}
		}
		out[i] = b.String()
	}
	return out
}

func TestWrapAnsiMatchesClaudeTrace(t *testing.T) {
	// Recorded at 40 columns (text width 36): typing "d" after "lazy " moved
	// the word to a new row and left the space on the first row.
	elems := TextElems("the quick brown fox jumps over lazy d")
	got := rowStrings(elems, WrapAnsi(elems, 36))
	want := []string{"the quick brown fox jumps over lazy ", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
	elems = TextElems("the quick brown fox jumps over lazy ")
	rows := WrapAnsi(elems, 36)
	if len(rows) != 1 {
		t.Fatalf("trailing space wrapped: %q", rowStrings(elems, rows))
	}
	if row, col := cursorRowCol(elems, rows, len(elems), 36); row != 0 || col != 36 {
		t.Fatalf("cursor after trailing space at %d,%d; the trace shows the end of row 0", row, col)
	}
}

func TestWrapAnsiHardBreaksLongWordsAndNewlines(t *testing.T) {
	elems := TextElems("abcdefghij\nxy")
	got := rowStrings(elems, WrapAnsi(elems, 4))
	want := []string{"abcd", "efgh", "ij", "xy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
	// CJK text has no spaces: wrap by width, two columns per character.
	elems = TextElems("你好世界和平")
	got = rowStrings(elems, WrapAnsi(elems, 5))
	want = []string{"你好", "世界", "和平"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK rows %q, want %q", got, want)
	}
}

func TestWrapAnsiEmptyAndTrailingNewline(t *testing.T) {
	if rows := WrapAnsi(nil, 10); len(rows) != 1 {
		t.Fatalf("empty text has %d rows", len(rows))
	}
	elems := TextElems("ab\n")
	rows := WrapAnsi(elems, 10)
	if len(rows) != 2 {
		t.Fatalf("trailing newline rows %q", rowStrings(elems, rows))
	}
	if row, col := cursorRowCol(elems, rows, len(elems), 10); row != 1 || col != 0 {
		t.Fatalf("cursor after newline at %d,%d", row, col)
	}
}

func TestWrapCodexHangingSpacesAndFullLine(t *testing.T) {
	elems := TextElems("hello world again")
	got := rowStrings(elems, WrapCodex(elems, 11))
	want := []string{"hello world ", "again"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
	// Exactly full line: an extra insertion row, cursor at its start.
	elems = TextElems("abcde")
	rows := WrapCodex(elems, 5)
	if len(rows) != 2 {
		t.Fatalf("full line rows %q", rowStrings(elems, rows))
	}
	if row, col := cursorRowCol(elems, rows, len(elems), 5); row != 1 || col != 0 {
		t.Fatalf("cursor at %d,%d", row, col)
	}
	elems = TextElems("abcdefgh")
	got = rowStrings(elems, WrapCodex(elems, 5))
	if !reflect.DeepEqual(got, []string{"abcde", "fgh"}) {
		t.Fatalf("long word rows %q", got)
	}
}
