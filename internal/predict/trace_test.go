package predict

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// traceChunk is one PTY read recorded by `script --log-timing`.
type traceChunk struct {
	At   float64
	Data []byte
}

func loadTrace(t *testing.T, name string) []traceChunk {
	t.Helper()
	dir := filepath.Join("testdata", "traces", name)
	data, err := os.ReadFile(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	timing, err := os.Open(filepath.Join(dir, "timing"))
	if err != nil {
		t.Fatal(err)
	}
	defer timing.Close()
	var chunks []traceChunk
	at, offset := 0.0, 0
	scanner := bufio.NewScanner(timing)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		delta, _ := strconv.ParseFloat(fields[0], 64)
		n, _ := strconv.Atoi(fields[1])
		at += delta
		chunks = append(chunks, traceChunk{At: at, Data: data[offset : offset+n]})
		offset += n
	}
	return chunks
}

// replayUntil feeds every chunk recorded before the given time.
func replayUntil(s *Screen, chunks []traceChunk, until float64) {
	for _, c := range chunks {
		if c.At > until {
			return
		}
		s.Write(c.Data)
	}
}

func dumpScreen(s *Screen) string {
	var b strings.Builder
	for r := 0; r < s.Rows; r++ {
		b.WriteString(strings.TrimRight(s.RowText(r, 0, s.Cols), " "))
		b.WriteByte('\n')
	}
	return b.String()
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
