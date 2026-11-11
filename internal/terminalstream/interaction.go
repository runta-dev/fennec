package terminalstream

import "bytes"

// InteractionEncoder separates focus reports from human keystrokes, preserving
// byte order and bracketed paste. The terminal input decoder has already joined
// fragmented escape sequences before these bytes reach this boundary.
type InteractionEncoder struct{ pasted bool }

func (e *InteractionEncoder) Frames(data []byte) []Frame {
	var frames []Frame
	start := 0
	for i := 0; i < len(data); i++ {
		if bytes.HasPrefix(data[i:], []byte("\x1b[200~")) {
			e.pasted = true
			i += 5
			continue
		}
		if bytes.HasPrefix(data[i:], []byte("\x1b[201~")) {
			e.pasted = false
			i += 5
			continue
		}
		if !e.pasted && i+3 <= len(data) && data[i] == 27 && data[i+1] == '[' && (data[i+2] == 'I' || data[i+2] == 'O') {
			if i > start {
				frames = append(frames, Frame{Type: "input", Data: data[start:i]})
			}
			frames = append(frames, Frame{Type: "focus", Focused: data[i+2] == 'I'})
			i += 2
			start = i + 1
		}
	}
	if start < len(data) {
		frames = append(frames, Frame{Type: "input", Data: data[start:]})
	}
	return frames
}

// Herdr's attach client turns these reports into native FOCUS frames on the
// core connection; it does not send them as user typing to the program.
func FocusReport(focused bool) []byte {
	if focused {
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}
