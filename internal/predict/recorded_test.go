package predict

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type recordedKey struct {
	at  float64
	raw []byte
}

// loadInput reads a recorder's "input" file: "<seconds> <hex>" per packet, on
// the same clock as the output timing.
func loadInput(t *testing.T, name string) []recordedKey {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "traces", name, "input"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var keys []recordedKey
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		at, _ := strconv.ParseFloat(fields[0], 64)
		raw, err := hex.DecodeString(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, recordedKey{at: at, raw: raw})
	}
	return keys
}

// TestCodexRecordedSession replays a real Codex session recorded through fen
// (typing, wrapping, Shift+Enter, Backspace, Left, insertion, bracketed
// paste) with its real timing and link latency. Every key the profile
// predicts must show exactly what Codex later drew, at once, and the terminal
// must converge to Codex's screen.
func TestCodexRecordedSession(t *testing.T) {
	const name, rows, cols = "codex-100x30", 30, 100
	chunks := loadTrace(t, name)
	keys := loadInput(t, name)
	now := time.Unix(0, 0)
	at := func(sec float64) time.Time { return time.Unix(0, int64(sec*1e9)) }
	var term bytes.Buffer
	engine := New(&term, newCodexProfile(), rows, cols, Options{noTick: true, Mode: ModeAlways, Now: func() time.Time { return now }})
	engine.trusted = true
	defer engine.Close()
	// The truth for each key: Codex's screen once its echo for that key and
	// all earlier keys has arrived, i.e. just before the next key's echo.
	observedStates := composerStates(t, name, rows, cols, newCodexProfile())
	truthAfter := func(until float64) *Screen {
		s := NewScreen(rows, cols)
		replayUntil(s, chunks, until)
		return s
	}
	_ = truthAfter
	ci, predicted, exact := 0, 0, 0
	for i, k := range keys {
		for ci < len(chunks) && chunks[ci].At <= k.at {
			now = at(chunks[ci].At)
			engine.Server(chunks[ci].Data)
			ci++
		}
		now = at(k.at)
		engine.Input(k.raw)
		engine.mu.Lock()
		shown := engine.display.Clone()
		painted := len(engine.painted) > 0
		engine.mu.Unlock()
		if !painted {
			continue
		}
		predicted++
		// What the user sees now must be a composer Codex shows later (keys
		// are typed faster than the round trip, so not necessarily the next
		// frame), drawn identically.
		mine, ok := newCodexProfile().Locate(shown)
		if !ok {
			t.Errorf("key %q at %.3f: prediction is not a composer\n%s", k.raw, k.at, dumpScreen(shown))
			continue
		}
		matched := false
		for _, o := range observedStates {
			if o.at > k.at && newCodexProfile().Same(mine.State, o.lock.State, o.screen) {
				if !composerRowsEqual(shown, o.screen, o.lock) {
					t.Errorf("key %q at %.3f: shown\n%s\nCodex drew\n%s", k.raw, k.at, dumpScreen(shown), dumpScreen(o.screen))
				}
				matched = true
				break
			}
		}
		if matched {
			exact++
		} else {
			t.Errorf("key %q at %.3f: Codex never showed the predicted %s", k.raw, k.at, describe(mine.State))
		}
		_ = i
	}
	for ; ci < len(chunks); ci++ {
		now = at(chunks[ci].At)
		engine.Server(chunks[ci].Data)
	}
	now = now.Add(5 * time.Second)
	engine.Ack(1<<62, false)
	engine.mu.Lock()
	ok, why := gridEqual(engine.display, engine.shadow)
	engine.mu.Unlock()
	if !ok {
		t.Fatalf("terminal did not converge: %s", why)
	}
	stats := engine.Stats()
	t.Logf("keys=%d predicted=%d exact=%d confirmed=%d diverged=%d expired=%d p50=%v p95=%v", len(keys), predicted, exact, stats.Confirmed, stats.Diverged, stats.Expired, stats.ConfirmP50, stats.ConfirmP95)
	if predicted < len(keys)/2 || exact != predicted {
		t.Fatalf("%d of %d shown predictions matched Codex", exact, predicted)
	}
}
