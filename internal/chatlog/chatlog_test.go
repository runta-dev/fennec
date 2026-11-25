package chatlog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"fennec/internal/transcript"
)

func TestTranscriptWithoutLogWaitsWithoutResettingHistory(t *testing.T) {
	for _, offset := range []int64{-1, 8192} {
		// No connection is needed until the agent creates its first log.
		raw, err := (&Reader{}).ReadPath("terminal", "session", "", offset)
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
	if got := PiSessions("@123\npi /root/.pi/agent /root/.fen/fly-ink\n"); got != "/root/.pi/agent/sessions/--root-.fen-fly-ink--" {
		t.Fatalf("got %q", got)
	}
	if PiSessions("open /x.jsonl") != "" {
		t.Fatal("no pi line")
	}
}

func TestParseKeptLines(t *testing.T) {
	user := `{"type":"user","message":{"role":"user","content":"hi"}}`
	agent := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello."}]}}`
	// First read from offset 100 of a 400-byte log; the last line is unfinished.
	raw := "400 100\n10 " + user + "\n200 " + agent + "\n@end 301 101\n"
	_, start, items, position, err := ParseKeptLines([]byte(raw), -1, MaxRead)
	if err != nil || start != 100 || len(items) != 1 || items[0].Kind != "user" || position != 300 {
		t.Fatalf("start %d items %+v position %d err %v", start, items, position, err)
	}
	// Incremental read: all lines complete.
	raw = "500 351\n0 " + agent + "\n@end 149 149\n"
	_, _, items, position, _ = ParseKeptLines([]byte(raw), 351, MaxRead)
	if len(items) != 1 || items[0].Kind != "agent" || position != 500 {
		t.Fatalf("items %+v position %d", items, position)
	}
}

// A line longer than a read (a message carrying a screenshot) is not stepped
// over: the position stays put so it is read again whole. Only a line longer
// than the largest read is skipped.
func TestLongLineIsReadAgainNotSkipped(t *testing.T) {
	partial := strings.Repeat("x", 100)
	raw := fmt.Sprintf("5100 0\n0 %s\n@end 101 101\n", partial)
	_, _, items, position, err := ParseKeptLines([]byte(raw), 0, 100)
	if err != nil || position != 0 || len(items) != 0 {
		t.Fatalf("position %d items %v err %v", position, items, err)
	}
	// Longer than even the largest read: stepped over, never stuck.
	huge := fmt.Sprintf("%d 0\n0 x\n@end %d %d\n", 2*MaxLine, MaxLine+1, MaxLine+1)
	_, _, _, position, _ = ParseKeptLines([]byte(huge), 0, MaxLine)
	if position != MaxLine {
		t.Fatalf("huge line not stepped over: %d", position)
	}
}

func TestLazyImagesAreFetchedByReference(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/log.jsonl"
	first := `{"type":"user","message":{"role":"user","content":"hi"}}`
	withImage := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}}`
	if err := os.WriteFile(path, []byte(first+"\n"+withImage+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := Local()
	reader.LazyImages = true
	raw, err := reader.ReadPath("t1", "abc-def", path, -1)
	if err != nil {
		t.Fatal(err)
	}
	var chunk struct{ Items []transcript.Item }
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil || len(chunk.Items) != 2 || len(chunk.Items[1].Images) != 1 {
		t.Fatalf("%s %v", raw, err)
	}
	ref := chunk.Items[1].Images[0]
	if strings.Contains(raw, "iVBORw0KGgo") || !strings.HasPrefix(ref, RefPrefix) {
		t.Fatalf("image data sent with the chat: %s", raw)
	}
	gotPath, id, index, err := ParseImageRef(ref)
	if err != nil || gotPath != path {
		t.Fatalf("%q %v", gotPath, err)
	}
	url, err := Image(gotPath, id, index)
	if err != nil || url != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("%q %v", url, err)
	}
	if _, err := Image(path, id, 1); err == nil {
		t.Fatal("missing image index must fail")
	}
	for _, bad := range []string{"fennec-log-image:1:0:relative.jsonl", "fennec-log-image:x:0:/a.jsonl", "fennec-log-image:1:0:/etc/passwd"} {
		if _, _, _, err := ParseImageRef(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
