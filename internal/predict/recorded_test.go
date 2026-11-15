package predict

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"io/fs"
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

type recordedAck struct {
	at    float64
	seq   uint64
	quiet bool
}

// loadInput reads a recorder's "input" file: "<seconds> <hex>" per packet, on
// the same clock as the output timing.
func loadInput(t *testing.T, name string) []recordedKey {
	t.Helper()
	var keys []recordedKey
	for _, fields := range loadLines(t, name, "input") {
		at, _ := strconv.ParseFloat(fields[0], 64)
		raw, err := hex.DecodeString(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, recordedKey{at: at, raw: raw})
	}
	return keys
}

// loadAcks reads the cloud's echo acknowledgements, if the recording has them.
func loadAcks(t *testing.T, name string) []recordedAck {
	t.Helper()
	var acks []recordedAck
	for _, fields := range loadLines(t, name, "acks") {
		at, _ := strconv.ParseFloat(fields[0], 64)
		seq, _ := strconv.ParseUint(fields[1], 10, 64)
		acks = append(acks, recordedAck{at: at, seq: seq, quiet: len(fields) > 2 && fields[2] == "true"})
	}
	return acks
}

func loadLines(t *testing.T, name, file string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "traces", name, file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) >= 2 {
			out = append(out, fields)
		}
	}
	return out
}

type replayResult struct {
	keys, predicted, exact, paused int
	stats                          Stats
}

// replayRecorded drives the engine with a real session recorded through fen:
// keys, output and acknowledgements at their recorded times (so with the
// real link latency). Each prediction shown must match a composer the agent
// later drew (the whole screen when fullScreen), and text typed into a locked
// composer must always be shown at once.
func replayRecorded(t *testing.T, name string, rows, cols int, profile func() Profile, fullScreen bool) replayResult {
	t.Helper()
	chunks := loadTrace(t, name)
	keys := loadInput(t, name)
	acks := loadAcks(t, name)
	observedStates := composerStates(t, name, rows, cols, profile())
	now := time.Unix(0, 0)
	at := func(sec float64) time.Time { return time.Unix(0, int64(sec*1e9)) }
	var term bytes.Buffer
	engine := New(&term, profile(), rows, cols, Options{noTick: true, Mode: ModeAlways, EchoAck: len(acks) > 0, Now: func() time.Time { return now }})
	engine.trusted = true
	defer engine.Close()
	var res replayResult
	res.keys = len(keys)
	ci, ai := 0, 0
	advance := func(until float64) {
		for {
			nextChunk, nextAck := 1e18, 1e18
			if ci < len(chunks) {
				nextChunk = chunks[ci].At
			}
			if ai < len(acks) {
				nextAck = acks[ai].at
			}
			if (ci == len(chunks) && ai == len(acks)) || min(nextChunk, nextAck) > until {
				return
			}
			if nextChunk <= nextAck {
				now = at(nextChunk)
				engine.Server(chunks[ci].Data)
				ci++
			} else {
				now = at(nextAck)
				engine.Ack(acks[ai].seq, acks[ai].quiet)
				ai++
			}
		}
	}
	for _, k := range keys {
		advance(k.at)
		now = at(k.at)
		engine.mu.Lock()
		locked := engine.locked && len(engine.pending) == 0 || (engine.locked && !engine.pending[len(engine.pending)-1].blocked())
		engine.mu.Unlock()
		barriers := engine.Stats().Barriers
		engine.Input(k.raw)
		barrier := engine.Stats().Barriers > barriers
		engine.mu.Lock()
		shown := engine.display.Clone()
		painted := len(engine.painted) > 0
		engine.mu.Unlock()
		typed := false
		for _, key := range ParseKeys(k.raw) {
			typed = typed || key.Kind == KeyText
		}
		if !painted {
			if locked && typed && !barrier {
				res.paused++
				t.Errorf("text %q at %.3f was typed into a locked composer but not shown", k.raw, k.at)
			}
			continue
		}
		res.predicted++
		mine, ok := profile().Locate(shown)
		if !ok {
			t.Errorf("key %q at %.3f: prediction is not a composer\n%s", k.raw, k.at, dumpScreen(shown))
			continue
		}
		matched := false
		for _, o := range observedStates {
			if o.at <= k.at || !profile().Same(mine.State, o.lock.State, o.screen) {
				continue
			}
			equal := composerRowsEqual(shown, o.screen, o.lock)
			if fullScreen {
				equal = dumpScreen(shown) == dumpScreen(o.screen)
			}
			if !equal {
				t.Errorf("key %q at %.3f: shown\n%s\nagent drew\n%s", k.raw, k.at, dumpScreen(shown), dumpScreen(o.screen))
			}
			matched = true
			break
		}
		if matched {
			res.exact++
		} else {
			t.Errorf("key %q at %.3f: agent never showed the predicted %s", k.raw, k.at, describe(mine.State))
		}
	}
	advance(1e18)
	now = now.Add(5 * time.Second)
	engine.Ack(0, false)
	engine.mu.Lock()
	ok, why := gridEqual(engine.display, engine.shadow)
	engine.mu.Unlock()
	if !ok {
		t.Fatalf("terminal did not converge: %s", why)
	}
	res.stats = engine.Stats()
	t.Logf("keys=%d predicted=%d exact=%d paused=%d confirmed=%d diverged=%d expired=%d p50=%v p95=%v", res.keys, res.predicted, res.exact, res.paused, res.stats.Confirmed, res.stats.Diverged, res.stats.Expired, res.stats.ConfirmP50, res.stats.ConfirmP95)
	if res.exact != res.predicted {
		t.Fatalf("%d of %d shown predictions matched the agent", res.exact, res.predicted)
	}
	return res
}

// TestCodexRecordedSession replays a real Codex session (typing, wrapping,
// Shift+Enter, Backspace, Left, insertion, bracketed paste).
func TestCodexRecordedSession(t *testing.T) {
	res := replayRecorded(t, "codex-100x30", 30, 100, func() Profile { return newCodexProfile() }, false)
	if res.predicted < res.keys/2 {
		t.Fatalf("only %d of %d key packets predicted", res.predicted, res.keys)
	}
}

// TestClaudeTranscriptRecordedSession replays a real Claude Code session with
// a full transcript above the composer: typing that wraps twice, Shift+Enter
// (the hint row appears) and Backspace back to two rows. Each growth scrolls
// the transcript up and each shrink scrolls it back; the whole predicted
// screen must equal Claude Code's frame, with the cloud's real
// acknowledgements.
func TestClaudeTranscriptRecordedSession(t *testing.T) {
	res := replayRecorded(t, "claude-transcript-100x30", 30, 100, func() Profile { return newClaudeProfile() }, true)
	if res.stats.Diverged != 0 {
		t.Fatalf("%d rollbacks in a session where every prediction was right", res.stats.Diverged)
	}
	if res.predicted < 150 {
		t.Fatalf("only %d of %d key packets predicted", res.predicted, res.keys)
	}
}
