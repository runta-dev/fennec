package predict

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeClaude renders a Claude-style composer from a state, as Ink would:
// absolute moves, full rows, cursor last, inside a synchronized update.
type fakeClaude struct {
	rows, cols int
	screen     *Screen
	profile    Profile
	truth      State
	init       []byte
}

// newFakeCodex draws the Codex fullscreen layout from the spec: transcript,
// gap, tinted composer block (pad, › prompt with dim placeholder, pad), then
// the status line and hints at the bottom.
func newFakeCodex(rows, cols int) *fakeClaude {
	f := &fakeClaude{rows: rows, cols: cols, screen: NewScreen(rows, cols), profile: newCodexProfile()}
	tint := "\x1b[48;2;40;40;40m"
	fill := func(row int) string {
		return fmt.Sprintf("\x1b[%d;1H%s%s\x1b[0m", row, tint, strings.Repeat(" ", cols))
	}
	var b strings.Builder
	b.WriteString("\x1b[?1049h\x1b[2J\x1b[1;1H• Ran the tests\x1b[2;1H  all passing")
	b.WriteString(fill(rows-4) + fill(rows-3) + fill(rows-2))
	fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[1m›\x1b[22m \x1b[2mAsk Codex to do anything\x1b[0m", rows-3, tint)
	fmt.Fprintf(&b, "\x1b[%d;3Hgpt default · /tmp/project\x1b[%d;3H? for shortcuts\x1b[%d;3H\x1b[?25h", rows-1, rows, rows-3)
	f.screen.Write([]byte(b.String()))
	f.init = []byte(b.String())
	return f
}

func newFakeClaude(rows, cols int) *fakeClaude {
	f := &fakeClaude{rows: rows, cols: cols, screen: NewScreen(rows, cols), profile: newClaudeProfile()}
	border := strings.Repeat("─", cols)
	var b strings.Builder
	b.WriteString("\x1b[?1049h\x1b[2J")
	fmt.Fprintf(&b, "\x1b[1;1HClaude Code\x1b[%d;1H%s", rows-4, border)
	fmt.Fprintf(&b, "\x1b[%d;1H❯ \x1b[2mTry \"refactor\"\x1b[0m", rows-3)
	fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[%d;3H? for shortcuts\x1b[%d;3H\x1b[?25h", rows-2, border, rows-1, rows-3)
	f.screen.Write([]byte(b.String()))
	f.init = []byte(b.String())
	return f
}

// frame applies an edit to the server's truth and returns the bytes it draws.
func (f *fakeClaude) frame(edit EditFunc) []byte {
	lock, ok := f.lock()
	if !ok {
		panic("fake server lost its composer:\n" + dumpScreen(f.screen))
	}
	next := edit(lock.State)
	paint, ok := f.profile.Render(lock, next, f.screen)
	if !ok {
		return nil
	}
	f.truth = next
	var b bytes.Buffer
	b.WriteString("\x1b[?2026h\x1b[?25l")
	rows := make([]int, 0, len(paint.Rows))
	for r := range paint.Rows {
		rows = append(rows, r)
	}
	sort.Ints(rows)
	for _, r := range rows {
		fmt.Fprintf(&b, "\x1b[%d;1H", r+1)
		for _, c := range paint.Rows[r] {
			if c.Width == 0 {
				continue
			}
			b.WriteString(SGR(c.Attr))
			b.WriteString(c.Glyph())
		}
		b.WriteString("\x1b[0m")
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h\x1b[?2026l", paint.CursorRow+1, paint.CursorCol+1)
	f.screen.Write(b.Bytes())
	return b.Bytes()
}

func (f *fakeClaude) withInit() []byte { return f.init }

// lock finds the composer the way the agent knows it: from its own state.
func (f *fakeClaude) lock() (Lock, bool) {
	if lock, ok := f.profile.Match(f.screen, f.truth); ok {
		return lock, true
	}
	return f.profile.Locate(f.screen)
}

func gridEqual(a, b *Screen) (bool, string) {
	for r := 0; r < a.Rows; r++ {
		for c := 0; c < a.Cols; c++ {
			if a.Cell(r, c) != b.Cell(r, c) {
				return false, fmt.Sprintf("cell %d,%d: terminal %+v server %+v", r, c, a.Cell(r, c), b.Cell(r, c))
			}
		}
	}
	if a.Row != b.Row || a.Col != b.Col {
		return false, fmt.Sprintf("cursor terminal %d,%d server %d,%d", a.Row, a.Col, b.Row, b.Col)
	}
	return true, ""
}

var randomKeys = []string{"a", "b", "c", " ", "de", "你", "\x7f", "\x1b[D", "\x1b[C", "\x1b[13;2u", "\x01", "\x05", "\x1b[200~pasted words\x1b[201~", "\x15", "\x17"}

// TestEngineConvergesUnderRandomLatencyAndLoss types random keys while the
// server applies each one late, sometimes drops one (the agent ignored it),
// and acknowledges input. The terminal must equal the server screen whenever
// nothing is pending, and at the end.
func TestEngineConvergesUnderRandomLatencyAndLoss(t *testing.T) {
	agents := []struct {
		name    string
		server  func(rows, cols int) *fakeClaude
		profile func() Profile
	}{
		{"claude", newFakeClaude, func() Profile { return newClaudeProfile() }},
		{"codex", newFakeCodex, func() Profile { return newCodexProfile() }},
	}
	for _, agent := range agents {
		for seed := uint64(1); seed <= 40; seed++ {
			t.Run(fmt.Sprintf("%s/%d", agent.name, seed), func(t *testing.T) {
				convergeRandom(t, seed, agent.server, agent.profile())
			})
		}
	}
}

func convergeRandom(t *testing.T, seed uint64, newServer func(rows, cols int) *fakeClaude, profile Profile) {
	{
		{
			rng := rand.New(rand.NewPCG(seed, seed*7))
			rows, cols := 24, 40+int(seed%20)
			server := newServer(rows, cols)
			now := time.Unix(100, 0)
			var term bytes.Buffer
			engine := New(&term, profile, rows, cols, Options{noTick: true, Mode: ModeAlways, EchoAck: seed%2 == 0, Now: func() time.Time { return now }})
			engine.trusted = true
			defer engine.Close()
			engine.Server(server.withInit())
			type delivery struct {
				at    time.Time
				data  []byte
				seq   uint64
				quiet bool
			}
			var queue []delivery
			deliver := func(until time.Time) {
				for len(queue) > 0 && !queue[0].at.After(until) {
					d := queue[0]
					queue = queue[1:]
					now = d.at
					if len(d.data) > 0 {
						engine.Server(d.data)
					}
					engine.Ack(d.seq, d.quiet)
				}
			}
			for i := 0; i < 60; i++ {
				now = now.Add(time.Duration(30+rng.IntN(150)) * time.Millisecond)
				deliver(now)
				raw := randomKeys[rng.IntN(len(randomKeys))]
				seq := engine.Input([]byte(raw)) + uint64(len(raw))
				// The server applies the key with its own model, or ignores it.
				lock, ok := server.lock()
				if !ok {
					t.Fatalf("server composer lost after %q:\n%s cursor %d,%d", raw, dumpScreen(server.screen), server.screen.Row, server.screen.Col)
				}
				st := lock.State
				var data []byte
				dropped := rng.IntN(8) == 0
				if !dropped {
					for _, k := range ParseKeys([]byte(raw)) {
						if edit := server.profile.Edit(k, st, EditContext{}); edit != nil {
							st = edit(st)
						}
					}
					data = server.frame(func(State) State { return st })
				}
				latency := time.Duration(150+rng.IntN(350)) * time.Millisecond
				at := now.Add(latency)
				if n := len(queue); n > 0 && at.Before(queue[n-1].at) {
					at = queue[n-1].at
				}
				queue = append(queue, delivery{at: at, data: data, seq: seq, quiet: false})
				engine.mu.Lock()
				for r := 0; r < rows; r++ {
					if engine.painted[r] {
						continue
					}
					for c := 0; c < cols; c++ {
						if engine.display.Cell(r, c) != engine.shadow.Cell(r, c) {
							engine.mu.Unlock()
							t.Fatalf("step %d: unpredicted row %d differs from the server at col %d", i, r, c)
						}
					}
				}
				engine.mu.Unlock()
			}
			deliver(now.Add(time.Hour))
			now = now.Add(10 * time.Second)
			engine.Ack(1<<62, false)
			engine.mu.Lock()
			ok, why := gridEqual(engine.display, engine.shadow)
			pending := len(engine.pending)
			engine.mu.Unlock()
			if !ok || pending != 0 {
				t.Fatalf("not converged (%d pending): %s\nterminal:\n%s\nserver:\n%s", pending, why, dumpScreen(engine.display), dumpScreen(engine.shadow))
			}
			// The bytes the user's terminal received reproduce the same screen.
			replay := NewScreen(rows, cols)
			replay.Write(term.Bytes())
			if ok, why := gridEqual(replay, engine.shadow); !ok {
				t.Fatalf("terminal byte stream does not reproduce the server screen: %s", why)
			}
			stats := engine.Stats()
			if stats.Predicted == 0 {
				t.Fatalf("nothing was predicted: %+v\n%s", stats, dumpScreen(engine.shadow))
			}
		}
	}
}

func TestEngineRepairsDivergence(t *testing.T) {
	rows, cols := 20, 60
	server := newFakeClaude(rows, cols)
	now := time.Unix(100, 0)
	var term bytes.Buffer
	engine := New(&term, newClaudeProfile(), rows, cols, Options{noTick: true, Mode: ModeAlways, EchoAck: true, Now: func() time.Time { return now }})
	engine.trusted = true
	defer engine.Close()
	engine.Server(server.withInit())
	seq := engine.Input([]byte("x")) + 1
	engine.mu.Lock()
	if len(engine.painted) == 0 {
		engine.mu.Unlock()
		t.Fatal("prediction not shown")
	}
	engine.mu.Unlock()
	// The agent answered with something else entirely (e.g. a slash popup
	// swallowed the key and showed "y").
	now = now.Add(300 * time.Millisecond)
	engine.Server(server.frame(func(s State) State { return s.Insert(Elem{S: "y"}) }))
	engine.Ack(seq, false)
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if ok, why := gridEqual(engine.display, engine.shadow); !ok {
		t.Fatalf("divergence not repaired: %s", why)
	}
	if engine.stats.Diverged != 1 {
		t.Fatalf("diverged=%d", engine.stats.Diverged)
	}
}

func TestEngineHidesPredictionsWhenCursorHidden(t *testing.T) {
	rows, cols := 20, 60
	server := newFakeClaude(rows, cols)
	now := time.Unix(100, 0)
	var term bytes.Buffer
	engine := New(&term, newClaudeProfile(), rows, cols, Options{noTick: true, Mode: ModeAlways, Now: func() time.Time { return now }})
	engine.trusted = true
	defer engine.Close()
	engine.Server(server.withInit())
	// A dialog takes over: cursor hidden, composer gone.
	engine.Server([]byte("\x1b[?25l\x1b[2J\x1b[5;5HDo you trust this folder?"))
	engine.Input([]byte("y"))
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.painted) != 0 || engine.locked {
		t.Fatal("predicted into a dialog")
	}
}
