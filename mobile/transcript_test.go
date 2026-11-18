package fennecmobile

import (
	"encoding/json"
	"testing"
)

func TestTranscriptWithoutLogWaitsWithoutResettingHistory(t *testing.T) {
	for _, offset := range []int64{-1, 8192} {
		// No connection is needed until the agent creates its first log.
		raw, err := (&Sessions{}).readTranscript("terminal", "session", "", offset)
		if err != nil {
			t.Fatal(err)
		}
		var chunk struct {
			Items   []json.RawMessage `json:"items"`
			Offset  int64             `json:"offset"`
			Reset   bool              `json:"reset"`
			Path    *string           `json:"path"`
			Version *int              `json:"version"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.Items == nil || len(chunk.Items) != 0 || chunk.Offset != offset || chunk.Reset || chunk.Path != nil || chunk.Version != nil {
			t.Fatalf("waiting for a log must preserve the cursor and history: %s", raw)
		}
	}
}

func TestPiSessionsFolder(t *testing.T) {
	if got := piSessions("@123\npi /root/.pi/agent /root/.fen/fly-ink\n"); got != "/root/.pi/agent/sessions/--root-.fen-fly-ink--" {
		t.Fatalf("got %q", got)
	}
	if piSessions("open /x.jsonl") != "" {
		t.Fatal("no pi line")
	}
}

func TestParseKeptLines(t *testing.T) {
	user := `{"type":"user","message":{"role":"user","content":"hi"}}`
	agent := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello."}]}}`
	// First read from offset 100 of a 400-byte log; the last line is unfinished.
	raw := "400 100\n10 " + user + "\n200 " + agent + "\n@end 301 101\n"
	_, start, items, position, err := parseKeptLines([]byte(raw), -1)
	if err != nil || start != 100 || len(items) != 1 || items[0].Kind != "user" || position != 300 {
		t.Fatalf("start %d items %+v position %d err %v", start, items, position, err)
	}
	// Incremental read: all lines complete.
	raw = "500 351\n0 " + agent + "\n@end 149 149\n"
	_, _, items, position, _ = parseKeptLines([]byte(raw), 351)
	if len(items) != 1 || items[0].Kind != "agent" || position != 500 {
		t.Fatalf("items %+v position %d", items, position)
	}
}
