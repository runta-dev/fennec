package terminalstream

import (
	"time"
)

// TimedOutput is attachment output read At after the start of a recording.
type TimedOutput struct {
	At   time.Duration
	Data []byte
}

// SimSender describes the sender SimulateSender models.
type SimSender struct {
	// Interval: a take happens at most once per interval; output after a
	// quiet interval goes at once (0: whenever the sender is free).
	Interval time.Duration
	// Window: encoded bytes in flight per stream (0: unlimited). Like the
	// live sender, a take is emitted in OutputChunk pieces, each waiting for
	// window credit, and output read meanwhile waits in the conflator.
	Window int
	// RTT: credit for a piece returns a round trip after it left the link.
	RTT time.Duration
	// Rate: link bytes per second (the QoS pacer), first in first out.
	Rate float64
}

// SimResult is what SimulateSender sends from the counted start on.
type SimResult struct {
	Frames, Bytes int
	// Merged frames, the bytes they had and the bytes sent for them.
	Merged, MergedBytes, DiffBytes int
	// Arrived: when the last byte reached the client (half a round trip
	// after leaving the link), after the counted start.
	Arrived time.Duration
}

// encodedRatio: a 1 KiB output chunk is about 0.9 KiB on the wire after
// gzip, base64 and JSON (measured on the real link, docs/predictive-echo.md).
const encodedRatio = 0.9

// SimulateSender replays recorded output through the cloud's conflator and a
// modelled sender and link. Only takes at or after from are counted; earlier
// output sets up the screen. It measures what a sender would send; the link
// model ignores loss, other streams and the client's own processing.
func SimulateSender(rows, cols int, out []TimedOutput, from time.Duration, s SimSender) SimResult {
	return simulateSender(rows, cols, out, from, s, func([]byte) {})
}

func simulateSender(rows, cols int, out []TimedOutput, from time.Duration, s SimSender, sent func([]byte)) SimResult {
	c := newConflator(rows, cols)
	c.limit = 1 << 30
	var r SimResult
	var base SimResult // conflator statistics before from
	counting, closed := false, false
	type credit struct {
		at    time.Duration
		bytes int
	}
	var inflight []credit
	used := 0
	var t, linkFree, lastArrival time.Duration
	lastTake := time.Duration(-1 << 62) // the first output goes at once
	next := 0
	feed := func() { // output read by now
		for next < len(out) && out[next].At <= t {
			c.Write(out[next].Data)
			next++
		}
	}
	returnCredit := func() {
		for len(inflight) > 0 && inflight[0].at <= t {
			used -= inflight[0].bytes
			inflight = inflight[1:]
		}
	}
	for {
		feed()
		if len(c.queue) == 0 {
			if next < len(out) {
				t = max(t, out[next].At)
				continue
			}
			if closed {
				break
			}
			c.Close() // an unfinished segment goes as it is
			closed = true
			if len(c.queue) == 0 {
				break
			}
		}
		if due := lastTake + s.Interval; t < due {
			t = due
			feed()
		}
		if t >= from && !counting {
			counting = true
			base.Merged, base.MergedBytes, base.DiffBytes = c.Stats()
		}
		data := c.Take()
		lastTake = t
		sent(data)
		if counting {
			r.Frames++
			r.Bytes += len(data)
		}
		for len(data) > 0 {
			n := min(len(data), OutputChunk)
			data = data[n:]
			enc := int(float64(n)*encodedRatio) + 1
			returnCredit()
			for s.Window > 0 && used > 0 && used+enc > s.Window {
				t = max(t, inflight[0].at) // wait for the next credit
				returnCredit()
			}
			linkFree = max(linkFree, t) + time.Duration(float64(enc)/s.Rate*float64(time.Second))
			used += enc
			inflight = append(inflight, credit{linkFree + s.RTT, enc})
			if counting {
				lastArrival = linkFree + s.RTT/2
			}
		}
	}
	m, mb, db := c.Stats()
	if !counting {
		base.Merged, base.MergedBytes, base.DiffBytes = m, mb, db
	}
	r.Merged, r.MergedBytes, r.DiffBytes = m-base.Merged, mb-base.MergedBytes, db-base.DiffBytes
	if counting {
		r.Arrived = lastArrival - from
	}
	return r
}

// SimWindow is the terminal output window the live sender uses at a minimum
// round trip.
func SimWindow(rtt time.Duration) int { return adaptiveWindow(rtt) }
