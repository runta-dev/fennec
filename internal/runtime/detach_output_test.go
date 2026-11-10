package runtime

import (
	"strings"
	"testing"
)

func TestDetachTrailerAcrossEverySplit(t *testing.T) {
	input := "\x1b[0m\r\n[detached from t3x]\r\n"
	for split := 0; split <= len(input); split++ {
		var out strings.Builder
		w := detachOutput{output: &out, address: "t3x", id: "amber-cedar"}
		w.Write([]byte(input[:split]))
		w.Write([]byte(input[split:]))
		w.Flush()
		want := "\x1b[0m\r\nSession detached. Run fen attach amber-cedar to reattach it.\r\nOr run fen to manage sessions.\r\n"
		if out.String() != want {
			t.Fatalf("split %d: %q", split, out.String())
		}
	}
}
func TestDetachPreservesUnrelatedOutput(t *testing.T) {
	for _, input := range []string{"hello\r\n", "[detached from t3x]", "\r\n[detached from t4]\r\n", "\r\n[det"} {
		var out strings.Builder
		w := detachOutput{output: &out, address: "t3x", id: "amber-cedar"}
		w.Write([]byte(input))
		w.Flush()
		if out.String() != input {
			t.Fatal(out.String())
		}
	}
}
func TestDetachInlineCommands(t *testing.T) {
	var out strings.Builder
	w := detachOutput{output: &out, address: "t3x", id: "amber-cedar", styled: true}
	w.Write([]byte("\r\n[detached from t3x]\r\n"))
	w.Flush()
	if !strings.Contains(out.String(), "\x1b[7m fen attach amber-cedar \x1b[27m") {
		t.Fatal(out.String())
	}
}
