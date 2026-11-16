package launcher

import (
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testWheel = "\x1b[<64;50;15M"
	testHover = "\x1b[<35;10;5M"
	testClick = "\x1b[<0;10;5M"
)

type sentPacket struct {
	data  string
	stamp inputStamp
}

type recorder struct {
	mu      sync.Mutex
	packets []sentPacket
}

func (r *recorder) send(data []byte, stamp inputStamp) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.packets = append(r.packets, sentPacket{string(data), stamp})
	return nil
}

func (r *recorder) all() []sentPacket {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sentPacket(nil), r.packets...)
}

func (r *recorder) waitFor(t *testing.T, n int) []sentPacket {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := r.all(); len(p) >= n {
			return p
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("expected %d packets, got %+v", n, r.all())
	return nil
}

func TestParseMouse(t *testing.T) {
	reports, hover, ok := parseMouse([]byte(testWheel + testHover + testClick))
	if !ok || len(reports) != 3 || hover[0] || !hover[1] || hover[2] {
		t.Fatalf("reports %q hover %v ok %v", reports, hover, ok)
	}
	for _, other := range []string{"a", testWheel + "a", "\x1b[A", "\x1b[<64;50;15"} {
		if _, _, ok := parseMouse([]byte(other)); ok {
			t.Errorf("%q parsed as mouse reports", other)
		}
	}
}

// A wheel gesture: the first step goes at once, the rest wait for the cloud
// to answer it and then go together, in one packet with no typing gap and
// the sequence the reports ended at; no step is lost.
func TestWheelStepsCoalescedUntilAck(t *testing.T) {
	var r recorder
	c := newMouseCoalescer(r.send, func() bool { return true })
	seq := uint64(100)
	for i := 0; i < 10; i++ {
		if err := c.Send([]byte(testWheel), inputStamp{seq: seq, gap: 16 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		seq += uint64(len(testWheel))
	}
	if p := r.all(); len(p) != 1 || p[0].data != testWheel {
		t.Fatalf("before the acknowledgement: %+v", p)
	}
	c.Ack(100 + uint64(len(testWheel)))
	p := r.waitFor(t, 2)
	if p[1].data != strings.Repeat(testWheel, 9) || p[1].stamp.gap != 0 || p[1].stamp.seq+uint64(len(p[1].data)) != seq {
		t.Fatalf("burst %+v (end %d, want %d)", p[1], p[1].stamp.seq+uint64(len(p[1].data)), seq)
	}
	reports, sent, dropped, bursts := c.Stats()
	if reports != 10 || sent != 10 || dropped != 0 || bursts != 2 {
		t.Fatalf("stats %d %d %d %d", reports, sent, dropped, bursts)
	}
}

// Pointer moves with no button held: only the latest is kept; wheel steps
// and clicks around them are kept in order.
func TestHoverMovesSuperseded(t *testing.T) {
	var r recorder
	c := newMouseCoalescer(r.send, func() bool { return true })
	seq := uint64(0)
	send := func(s string) {
		if err := c.Send([]byte(s), inputStamp{seq: seq}); err != nil {
			t.Fatal(err)
		}
		seq += uint64(len(s))
	}
	send(testWheel)
	hover2 := "\x1b[<35;11;5M"
	hover3 := "\x1b[<35;12;5M"
	send(testHover)
	send(testWheel)
	send(hover2)
	send(testClick)
	send(hover3)
	c.Ack(uint64(len(testWheel)))
	p := r.waitFor(t, 2)
	if want := testWheel + testClick + hover3; p[1].data != want {
		t.Fatalf("burst %q, want %q", p[1].data, want)
	}
	if p[1].stamp.seq+uint64(len(p[1].data)) != seq {
		t.Fatalf("burst ends at %d, input at %d", p[1].stamp.seq+uint64(len(p[1].data)), seq)
	}
}

// Typing sends held mouse reports first, so the order is unchanged.
func TestKeysFlushHeldReports(t *testing.T) {
	var r recorder
	c := newMouseCoalescer(r.send, func() bool { return true })
	c.Send([]byte(testWheel), inputStamp{seq: 0})
	c.Send([]byte(testWheel), inputStamp{seq: 12})
	c.Send([]byte("x"), inputStamp{seq: 24, gap: time.Second})
	p := r.all()
	if len(p) != 3 || p[1].data != testWheel || p[2].data != "x" || p[2].stamp.gap != time.Second {
		t.Fatalf("packets %+v", p)
	}
}

// Without an acknowledgement, held reports go after maxHold; without
// acknowledgements at all, once per holdWindow.
func TestHoldBounded(t *testing.T) {
	for _, ack := range []bool{true, false} {
		var r recorder
		c := newMouseCoalescer(r.send, func() bool { return ack })
		c.maxHold, c.holdWindow = 60*time.Millisecond, 30*time.Millisecond
		c.Send([]byte(testWheel), inputStamp{seq: 0})
		c.Send([]byte(testWheel), inputStamp{seq: 12})
		c.Send([]byte(testWheel), inputStamp{seq: 24})
		started := time.Now()
		p := r.waitFor(t, 2)
		if p[1].data != testWheel+testWheel || time.Since(started) > time.Second {
			t.Fatalf("ack=%v: %+v", ack, p)
		}
	}
}

// Close sends what is held.
func TestCloseSendsHeld(t *testing.T) {
	var r recorder
	c := newMouseCoalescer(r.send, func() bool { return true })
	c.Send([]byte(testWheel), inputStamp{seq: 0})
	c.Send([]byte(testWheel), inputStamp{seq: 12})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if p := r.all(); len(p) != 2 {
		t.Fatalf("packets %+v", p)
	}
}
