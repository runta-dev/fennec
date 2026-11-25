package fennecmobile

import (
	"strings"
	"testing"
)

// An older fennec-terminal doesn't know `transcript`: the phone then reads
// the log step by step itself, and stops asking the cloud computer.
func TestTranscriptFallsBackOnOlderTerminal(t *testing.T) {
	if !strings.Contains("expected launch or attach", oldTerminal) {
		t.Fatal("marker must match the older fennec-terminal's error")
	}
	if _, err := (&Sessions{}).Transcript("not a terminal", "x", -1); err == nil {
		t.Fatal("invalid terminal must be refused before any command")
	}
}
