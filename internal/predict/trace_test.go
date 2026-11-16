package predict

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// replayUntil feeds every chunk recorded before the given time.
func replayUntil(s *Screen, chunks []traceChunk, until float64) {
	for _, c := range chunks {
		if c.At > until {
			return
		}
		s.Write(c.Data)
	}
}

func TestTraceDump(t *testing.T) {
	if os.Getenv("PREDICT_DUMP") == "" {
		t.Skip("set PREDICT_DUMP=name:rows:cols:time")
	}
	parts := strings.Split(os.Getenv("PREDICT_DUMP"), ":")
	rows, _ := strconv.Atoi(parts[1])
	cols, _ := strconv.Atoi(parts[2])
	until, _ := strconv.ParseFloat(parts[3], 64)
	s := NewScreen(rows, cols)
	replayUntil(s, loadTrace(t, parts[0]), until)
	t.Logf("cursor %d,%d visible=%v\n%s", s.Row, s.Col, s.CursorVisible, dumpScreen(s))
}

type traceChunk = TraceChunk

func loadTrace(t *testing.T, name string) []traceChunk {
	t.Helper()
	tr, err := LoadTrace(filepath.Join("testdata", "traces", name))
	if err != nil {
		t.Fatal(err)
	}
	return tr.Output
}

func dumpScreen(s *Screen) string { return DumpScreen(s) }
