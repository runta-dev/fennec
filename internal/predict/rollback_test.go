package predict

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// TestFastTypingNeverRollsBack simulates continuous English typing over a
// slow link against an agent that, like Codex and Ink, emits a small frame
// right after each key and draws the echo 10-30 ms later, while the cloud
// acknowledges input with the real rules (15 ms of quiet after any output,
// or 250 ms). The text the user sees must only ever grow.
func TestFastTypingNeverRollsBack(t *testing.T) {
	const sentence = "the quick brown fox jumps over the lazy dog while typing quickly"
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 99))
			const rows, cols = 24, 100
			oneWay := time.Duration(80+rng.IntN(70)) * time.Millisecond
			server := newFakeClaude(rows, cols)
			now := time.Unix(100, 0)
			var term bytes.Buffer
			engine := New(&term, newClaudeProfile(), rows, cols, Options{noTick: true, Mode: ModeAlways, EchoAck: true, Now: func() time.Time { return now }})
			engine.trusted = true
			defer engine.Close()
			engine.Server(server.withInit())

			type event struct {
				at   time.Time
				run  func()
				name string
			}
			var events []event
			schedule := func(at time.Time, name string, run func()) {
				i := len(events)
				for i > 0 && events[i-1].at.After(at) {
					i--
				}
				events = append(events, event{})
				copy(events[i+1:], events[i:])
				events[i] = event{at: at, run: run, name: name}
			}
			// Server side: truth, render coalescing, acknowledgements.
			if _, ok := server.lock(); !ok {
				t.Fatal("fake server has no composer")
			}
			truth := State{}
			var ackSeq uint64
			ackPending, outputSeen := false, false
			var writtenAt, outputAt time.Time
			renderPending := false
			emit := func(at time.Time, data []byte) {
				if ackPending {
					outputSeen, outputAt = true, at
				}
				schedule(at.Add(oneWay), "frame", func() { engine.Server(data) })
			}
			checkAck := func(at time.Time) {
				if !ackPending {
					return
				}
				if (outputSeen && at.Sub(outputAt) >= 15*time.Millisecond) || at.Sub(writtenAt) >= 250*time.Millisecond {
					ackPending = false
					seq, quiet := ackSeq, !outputSeen
					schedule(at.Add(oneWay), "ack", func() { engine.Ack(seq, quiet) })
				}
			}
			receive := func(at time.Time, raw string, seq uint64) {
				for _, k := range ParseKeys([]byte(raw)) {
					if edit := server.profile.Edit(k, truth, EditContext{}); edit != nil {
						truth = edit(truth)
					}
				}
				ackSeq, ackPending, outputSeen, writtenAt = seq, true, false, at
				emit(at.Add(time.Millisecond), []byte("\x1b[?2026h\x1b[?2026l")) // no visible change
				if !renderPending {
					renderPending = true
					delay := time.Duration(10+rng.IntN(21)) * time.Millisecond
					schedule(at.Add(delay), "render", func() {
						renderPending = false
						want := truth
						emit(now, server.frame(func(State) State { return want }))
					})
				}
			}
			// Typing: 30-130 ms between keys, sometimes two keys together.
			at := now
			for _, r := range sentence {
				gap := time.Duration(30+rng.IntN(100)) * time.Millisecond
				if rng.IntN(6) == 0 {
					gap = 3 * time.Millisecond
				}
				at = at.Add(gap)
				key := string(r)
				schedule(at, "type", func() {
					seq := engine.Input([]byte(key)) + uint64(len(key))
					sent := now
					schedule(sent.Add(oneWay), "receive", func() { receive(now, key, seq) })
				})
			}
			end := at.Add(3 * time.Second)
			for tick := now.Add(10 * time.Millisecond); !tick.After(end); tick = tick.Add(10 * time.Millisecond) {
				schedule(tick, "tick", func() { checkAck(now); engine.Ack(0, false) })
			}
			shown, rollbacks := 0, []string{}
			for len(events) > 0 {
				ev := events[0]
				events = events[1:]
				now = ev.at
				ev.run()
				engine.mu.Lock()
				lock, ok := newClaudeProfile().Locate(engine.display)
				engine.mu.Unlock()
				if !ok {
					continue
				}
				text := lock.State.Text()
				if !strings.HasPrefix(sentence, text) {
					t.Fatalf("showed %q, not a prefix of what was typed", text)
				}
				if len(text) < shown {
					rollbacks = append(rollbacks, fmt.Sprintf("%s: %d->%d chars", ev.name, shown, len(text)))
				}
				shown = len(text)
			}
			if shown != len(sentence) {
				t.Fatalf("ended showing %d of %d chars", shown, len(sentence))
			}
			if len(rollbacks) > 0 {
				t.Fatalf("visible text shrank %d times (one-way %v): %v", len(rollbacks), oneWay, rollbacks)
			}
		})
	}
}
