package terminalstream

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/internal/predict"
)

// TestSimulateSenderExact: whatever the simulated sender sends, at any
// interval, leaves the client on the screen the raw output draws, for every
// recorded agent session (with its own timing) and a fast scroll gesture.
func TestSimulateSenderExact(t *testing.T) {
	type recording struct {
		name       string
		rows, cols int
		out        []TimedOutput
	}
	var recs []recording
	dirs, _ := filepath.Glob("../predict/testdata/traces/*")
	for _, dir := range dirs {
		tr, err := predict.LoadTrace(dir)
		if err != nil {
			t.Fatal(err)
		}
		r := recording{name: filepath.Base(dir), rows: 30, cols: 100}
		fmt.Sscanf(filepath.Base(dir)[strings.LastIndex(filepath.Base(dir), "-")+1:], "%dx%d", &r.cols, &r.rows)
		for _, c := range tr.Output {
			r.out = append(r.out, TimedOutput{time.Duration(c.At * float64(time.Second)), c.Data})
		}
		recs = append(recs, r)
	}
	scroll := recording{name: "scroll", rows: 30, cols: 100, out: []TimedOutput{{0, []byte("\x1b[?1049h\x1b[2J")}}}
	for step := 0; step < 40; step++ {
		scroll.out = append(scroll.out, TimedOutput{time.Second + time.Duration(step)*8*time.Millisecond, []byte(codexLikeStep(500 - 3*step))})
	}
	recs = append(recs, scroll)
	for _, rec := range recs {
		reference := predict.NewScreen(rec.rows, rec.cols)
		for _, o := range rec.out {
			reference.Write(o.Data)
		}
		for _, window := range []int{0, 4 << 10, SimWindow(220 * time.Millisecond)} {
			for _, interval := range []time.Duration{0, 8 * time.Millisecond, 16 * time.Millisecond, 33 * time.Millisecond, 100 * time.Millisecond} {
				client := predict.NewScreen(rec.rows, rec.cols)
				s := SimSender{Interval: interval, Window: window, RTT: 220 * time.Millisecond, Rate: 112 << 10}
				simulateSender(rec.rows, rec.cols, rec.out, 0, s, func(b []byte) { client.Write(b) })
				if same, why := predict.SameScreen(client, reference); !same {
					t.Fatalf("%s, window %d, interval %v: %s", rec.name, window, interval, why)
				}
			}
		}
	}
	r0 := SimulateSender(30, 100, scroll.out, time.Second, SimSender{Rate: 112 << 10})
	r16 := SimulateSender(30, 100, scroll.out, time.Second, SimSender{Interval: 16 * time.Millisecond, Rate: 112 << 10})
	t.Logf("scroll at 8 ms per step: %d frames %d bytes unmerged, %d frames %d bytes at 16 ms", r0.Frames, r0.Bytes, r16.Frames, r16.Bytes)
	if r16.Frames >= r0.Frames || r16.Bytes >= r0.Bytes {
		t.Fatalf("16 ms merged nothing: %+v vs %+v", r16, r0)
	}
}
