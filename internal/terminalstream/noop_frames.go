package terminalstream

import "bytes"

// Codex 0.169.0 answers each mouse wheel step with one frame that scrolls its
// transcript and then 90 to 140 synchronized-output frames that change
// nothing (pen reset, cursor to where it already is, cursor shown), within
// about 60 ms. A 40-step gesture is some 3,800 frames and 190 KB, which queue
// in front of the frames that matter on a slow link (cmd/profile-scroll).
//
// NoopFrameFilter drops a synchronized-output frame that changes no cell and
// is byte-identical to the frame emitted just before it, with nothing in
// between. Such a frame holds only SGR, absolute cursor positioning and
// cursor visibility, all of which are absolute: applying it again leaves the
// terminal exactly as it was.
//
// Herdr writes its output in chunks that do not follow frame boundaries, so
// an unfinished frame that so far could still be a no-op is held until the
// next read. That delays nothing visible: a terminal shows a synchronized
// frame only once it ends. Anything else (text, other sequences, frames
// over maxNoopFrame) passes at once.
type NoopFrameFilter struct {
	last  []byte // the last emitted no-op frame, if nothing followed it
	carry []byte // an unfinished frame, or the start of one, held for the next read
}

var (
	syncBegin = []byte("\x1b[?2026h")
	syncEnd   = []byte("\x1b[?2026l")
)

// maxNoopFrame bounds the frames examined; no-op frames are a few dozen bytes.
const maxNoopFrame = 256

// Filter returns the output to send now, without repeated no-op frames, and
// how many bytes it dropped. Bytes it holds are returned by a later call or
// by Flush.
func (f *NoopFrameFilter) Filter(data []byte) ([]byte, int) {
	if len(f.carry) > 0 {
		data = append(f.carry, data...)
		f.carry = nil
	} else if !bytes.Contains(data, []byte{0x1b}) {
		if len(data) > 0 {
			f.last = nil
		}
		return data, 0
	}
	out := make([]byte, 0, len(data))
	dropped := 0
	emit := func(b []byte) {
		if len(b) > 0 {
			out = append(out, b...)
			f.last = nil
		}
	}
	for len(data) > 0 {
		i := bytes.Index(data, syncBegin)
		if i < 0 {
			// The read may end with the start of the next frame.
			keep := partialPrefix(data, syncBegin)
			emit(data[:len(data)-keep])
			if keep > 0 {
				f.carry = append([]byte(nil), data[len(data)-keep:]...)
			}
			break
		}
		emit(data[:i])
		data = data[i:]
		end := bytes.Index(data[len(syncBegin):], syncEnd)
		if end < 0 {
			if len(data) <= maxNoopFrame+len(syncEnd) && onlyControls(data[len(syncBegin):], true) {
				f.carry = append([]byte(nil), data...)
			} else {
				emit(data)
			}
			break
		}
		frame := data[:len(syncBegin)+end+len(syncEnd)]
		data = data[len(frame):]
		body := frame[len(syncBegin) : len(frame)-len(syncEnd)]
		if len(body) > maxNoopFrame || !onlyControls(body, false) {
			emit(frame)
			continue
		}
		if f.last != nil && bytes.Equal(frame, f.last) {
			dropped += len(frame)
			continue
		}
		out = append(out, frame...)
		f.last = append(f.last[:0], frame...)
	}
	return out, dropped
}

// Flush returns held bytes (at the end of the stream).
func (f *NoopFrameFilter) Flush() []byte {
	data := f.carry
	f.carry, f.last = nil, nil
	return data
}

// partialPrefix is the length of the longest suffix of data that begins
// prefix without completing it.
func partialPrefix(data, prefix []byte) int {
	for n := min(len(data), len(prefix)-1); n > 0; n-- {
		if bytes.HasPrefix(prefix, data[len(data)-n:]) {
			return n
		}
	}
	return 0
}

// onlyControls: body is only SGR (CSI … m), absolute cursor position (CSI
// row;col H) and cursor visibility (CSI ?25 h/l). With partial, body may end
// inside a sequence (including the start of the frame's end).
func onlyControls(body []byte, partial bool) bool {
	for len(body) > 0 {
		if body[0] != 0x1b {
			return false
		}
		if len(body) == 1 {
			return partial
		}
		if body[1] != '[' {
			return false
		}
		j := 2
		private := j < len(body) && body[j] == '?'
		if private {
			j++
		}
		start := j
		for j < len(body) && (body[j] >= '0' && body[j] <= '9' || body[j] == ';' || body[j] == ':') {
			j++
		}
		if j == len(body) {
			return partial
		}
		params, final := string(body[start:j]), body[j]
		switch {
		case private && params == "25" && (final == 'h' || final == 'l'):
		case !private && final == 'm':
		case !private && final == 'H':
		default:
			return false
		}
		body = body[j+1:]
	}
	return true
}
