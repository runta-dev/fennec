package terminalstream

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"fennec/internal/predict"
)

// TestConflatorRecordedSessions replays every recorded agent session (Herdr
// attachment output) through the conflator with a sender that falls behind
// at random. After every take the client shows exactly what the output read
// so far shows, and at the end exactly the same screen.
func TestConflatorRecordedSessions(t *testing.T) {
	dirs, _ := filepath.Glob("../predict/testdata/traces/*")
	if len(dirs) < 20 {
		t.Fatalf("only %d recordings", len(dirs))
	}
	var merged, mergedBytes, diffBytes, total int
	for _, dir := range dirs {
		tr, err := predict.LoadTrace(dir)
		if err != nil {
			t.Fatal(err)
		}
		rows, cols := 30, 100
		fmt.Sscanf(strings.TrimPrefix(filepath.Base(dir)[strings.LastIndex(filepath.Base(dir), "-")+1:], ""), "%dx%d", &cols, &rows)
		for seed := int64(1); seed <= 3; seed++ {
			rng := rand.New(rand.NewSource(seed))
			c := newConflator(rows, cols)
			c.limit = 1 << 30
			reference := predict.NewScreen(rows, cols)
			client := predict.NewScreen(rows, cols)
			var readStream, sentStream []byte
			for _, chunk := range tr.Output {
				data := chunk.Data
				for len(data) > 0 {
					n := min(len(data), 1+rng.Intn(4096))
					c.Write(data[:n])
					reference.Write(data[:n])
					readStream = append(readStream, data[:n]...)
					total += n
					data = data[n:]
					if rng.Intn(6) == 0 && len(c.queue) > 0 {
						out := c.Take()
						client.Write(out)
						sentStream = append(sentStream, out...)
						if len(c.seg) == 0 && len(c.scan.buf) == 0 {
							if same, why := predict.SameScreen(client, reference); !same {
								t.Fatalf("%s seed %d: after a take: %s", filepath.Base(dir), seed, why)
							}
							if !sameStates(readStream, sentStream) {
								t.Fatalf("%s seed %d: after a take: state assignments differ", filepath.Base(dir), seed)
							}
						}
					}
				}
			}
			c.Close()
			for out := c.Take(); out != nil; out = c.Take() {
				client.Write(out)
				sentStream = append(sentStream, out...)
			}
			if same, why := predict.SameScreen(client, reference); !same {
				t.Fatalf("%s seed %d: at the end: %s", filepath.Base(dir), seed, why)
			}
			if !sameStates(readStream, sentStream) {
				t.Fatalf("%s seed %d: at the end: state assignments differ", filepath.Base(dir), seed)
			}
			m, mb, db := c.Stats()
			merged, mergedBytes, diffBytes = merged+m, mergedBytes+mb, diffBytes+db
		}
	}
	t.Logf("%d recordings x 3: %d bytes read, %d frames merged (%d bytes sent as %d)", len(dirs), total, merged, mergedBytes, diffBytes)
	if merged == 0 || diffBytes >= mergedBytes {
		t.Fatalf("nothing merged usefully")
	}
}

// codexLikeStep is a full redraw like the one Codex makes for a wheel step,
// followed by the no-op frames it repeats.
func codexLikeStep(top int) string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for r := 0; r < 29; r++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[38;5;%dm%5d\x1b[0m transcript line %d explaining a change in some detail\x1b[K", r+1, 30+(top+r)%200, top+r, top+r)
	}
	b.WriteString("\x1b[30;1H\x1b[0m> \x1b[30;3H\x1b[?25h\x1b[?2026l")
	b.WriteString(strings.Repeat("\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[30;3H\x1b[?25h\x1b[?2026l", 90))
	return b.String()
}

// TestConflatorScrollGesture: 40 wheel steps drawn while the sender is busy
// (one take per round trip) are sent as a few differences, and the client
// ends on the same screen.
func TestConflatorScrollGesture(t *testing.T) {
	c := newConflator(30, 100)
	reference := predict.NewScreen(30, 100)
	client := predict.NewScreen(30, 100)
	start := "\x1b[?1049h\x1b[2J"
	c.Write([]byte(start))
	reference.Write([]byte(start))
	client.Write(c.Take())
	raw, sent := 0, 0
	for step := 0; step < 40; step++ {
		frame := codexLikeStep(500 - 3*step)
		c.Write([]byte(frame))
		reference.Write([]byte(frame))
		raw += len(frame)
		if step%10 == 9 { // about one take per 250 ms at 16 ms per step
			out := c.Take()
			sent += len(out)
			client.Write(out)
		}
	}
	if same, why := predict.SameScreen(client, reference); !same {
		t.Fatal(why)
	}
	t.Logf("40 steps: %d bytes drawn, %d sent", raw, sent)
	if sent*5 > raw {
		t.Fatalf("sent %d of %d bytes", sent, raw)
	}
}

// TestConflatorKeepsOtherOutput: a title, a mode change and a bell between
// frames are sent in their place, and nothing is merged across them in a way
// that loses them.
func TestConflatorKeepsOtherOutput(t *testing.T) {
	c := newConflator(10, 40)
	c.Write([]byte("\x1b[?1049h"))
	c.Take()
	frame := func(s string) string { return "\x1b[?2026h\x1b[1;1H" + s + "\x1b[?2026l" }
	c.Write([]byte(frame("one") + frame("two") + "\x1b]0;title\x07" + frame("three") + "\x1b[?1000h" + frame("four") + "\a" + frame("five") + frame("six")))
	out := string(c.Take())
	for _, want := range []string{"\x1b]0;title\x07", "\x1b[?1000h", "\a"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q lost from %q", want, out)
		}
	}
	if strings.Index(out, "title") > strings.Index(out, "\x1b[?1000h") || strings.Index(out, "\x1b[?1000h") > strings.Index(out, "\x1b[?2026l\a") {
		t.Errorf("order changed: %q", out)
	}
	if strings.Contains(out, "one") && strings.Contains(out, "two") && !strings.Contains(out, "three") {
		t.Errorf("unexpected: %q", out)
	}
}

func TestSafeSGR(t *testing.T) {
	for p, want := range map[string]bool{"": true, "0": true, "1;31": true, "38;5;214": true, "48;2;1;2;3": true, "38:2::1:2:3": true, "4:0": true,
		"58;5;1": true, "58:2::9:8:7": true, "59": true, "4:59": false,
		"4:3": false, "58;5": false, "21": false, "6": false, "38;5": false, "53": false} {
		if got := safeSGR(p); got != want {
			t.Errorf("%q: %v", p, got)
		}
	}
}

// TestConflatorMergesHyperlinks: frames with OSC 8 links (Codex links URLs in
// its transcript) merge like any other cell change, the client ends with the
// same links, and a title between frames is still passed on.
func TestConflatorMergesHyperlinks(t *testing.T) {
	c := newConflator(30, 100)
	reference := predict.NewScreen(30, 100)
	client := predict.NewScreen(30, 100)
	start := "\x1b[?1049h\x1b[2J"
	c.Write([]byte(start))
	reference.Write([]byte(start))
	client.Write(c.Take())
	raw := 0
	for step := 0; step < 20; step++ {
		frame := strings.ReplaceAll(codexLikeStep(500-3*step), " explaining", " \x1b]8;;https://docs.example/"+fmt.Sprint(step)+"\x07explaining\x1b]8;;\x07")
		if step == 10 {
			frame += "\x1b]0;title\x07"
		}
		c.Write([]byte(frame))
		reference.Write([]byte(frame))
		raw += len(frame)
	}
	out := c.Take()
	client.Write(out)
	if same, why := predict.SameScreen(client, reference); !same {
		t.Fatal(why)
	}
	if !strings.Contains(string(out), "\x1b]0;title\x07") {
		t.Fatal("the title was lost")
	}
	if merged, _, _ := c.Stats(); merged < 18 || len(out)*4 > raw {
		t.Fatalf("%d frames merged, %d of %d bytes sent", merged, len(out), raw)
	}
}
