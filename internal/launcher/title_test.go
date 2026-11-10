package launcher

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionTitleFilterPreservesContentAcrossEverySplit(t *testing.T) {
	id := "219a43a1a2de5cc147206da1372251d3"
	for _, terminator := range []string{"\a", "\x1b\\"} {
		input := "\x1b[31mTEXT | " + id + "\x1b]2;⠋ Greet the user | " + id[:18] + "..." + terminator + "\x1b[0m"
		want := "\x1b[31mTEXT | " + id + "\x1b]2;⠋ Greet the user" + terminator + "\x1b[0m"
		for split := 0; split <= len(input); split++ {
			var output bytes.Buffer
			writer := SessionTitleWriter{Writer: &output, SessionID: id}
			writer.Write([]byte(input[:split]))
			writer.Write([]byte(input[split:]))
			writer.Flush()
			if output.String() != want {
				t.Fatalf("split %d: %q", split, output.String())
			}
		}
	}
}

func TestSessionTitleFilterOnlyRemovesOwnSessionSuffix(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, title := range []string{"Task | project", "Task | " + strings.Repeat("b", 32), "Task | aaaa...", id} {
		if CleanSessionTitle(title, id) != title {
			t.Fatal("unrelated title changed")
		}
	}
	if CleanSessionTitle("Task | "+id, id) != "Task" {
		t.Fatal("full ID retained")
	}
	var output bytes.Buffer
	writer := SessionTitleWriter{Writer: &output, SessionID: id}
	input := "\x1b]7;Task | " + id + "\a"
	writer.Write([]byte(input))
	writer.Flush()
	if output.String() != input {
		t.Fatal("non-title OSC changed")
	}
}
