package terminalstream

import (
	"bytes"
	"fennec/internal/terminalinput"
	"testing"
)

func TestInteractionFramesPreserveInputOrder(t *testing.T) {
	e := InteractionEncoder{}
	frames := e.Frames([]byte("abc\x1b[Idef\x1b[Oghi"))
	if len(frames) != 5 || frames[1].Type != "focus" || !frames[1].Focused || frames[3].Type != "focus" || frames[3].Focused {
		t.Fatal(frames)
	}
	var rebuilt []byte
	for _, frame := range frames {
		if frame.Type == "focus" {
			rebuilt = append(rebuilt, FocusReport(frame.Focused)...)
		} else {
			rebuilt = append(rebuilt, frame.Data...)
		}
	}
	if string(rebuilt) != "abc\x1b[Idef\x1b[Oghi" {
		t.Fatal(string(rebuilt))
	}
}
func TestInteractionFocusSurvivesEverySplit(t *testing.T) {
	for _, input := range []string{"\x1b[I", "\x1b[O", "x\x1b[Iy\x1b[Oz"} {
		for split := 0; split <= len(input); split++ {
			keys := terminalinput.Decoder{}
			events := InteractionEncoder{}
			var rebuilt []byte
			for _, part := range [][]byte{[]byte(input[:split]), []byte(input[split:])} {
				data, detach := keys.Feed(part)
				if detach {
					t.Fatal("focus detached")
				}
				for _, frame := range events.Frames(data) {
					if frame.Type == "focus" {
						rebuilt = append(rebuilt, FocusReport(frame.Focused)...)
					} else {
						rebuilt = append(rebuilt, frame.Data...)
					}
				}
			}
			if !bytes.Equal(rebuilt, []byte(input)) {
				t.Fatal(split, string(rebuilt))
			}
		}
	}
}
func TestPastedFocusBytesAreNotControlEvents(t *testing.T) {
	e := InteractionEncoder{}
	for _, data := range []string{"\x1b[200~", "text\x1b[I", "\x1b[201~"} {
		for _, frame := range e.Frames([]byte(data)) {
			if frame.Type != "input" {
				t.Fatal(frame)
			}
		}
	}
}

func TestImagePasteShortcutsAcrossEveryReadBoundary(t *testing.T) {
	for _, shortcut := range []string{"\x16", "\x1b[118;5u", "\x1b[118;5:2u", "\x1b[27;5;118~"} {
		input := "before" + shortcut + "after"
		for split := 0; split <= len(input); split++ {
			keys := terminalinput.Decoder{}
			events := InteractionEncoder{ImagePaste: true}
			var rebuilt []byte
			count := 0
			for _, part := range []string{input[:split], input[split:]} {
				data, _ := keys.Feed([]byte(part))
				for _, frame := range events.Frames(data) {
					if frame.Type == "clipboard" {
						count++
						if string(frame.Data) != shortcut {
							t.Fatal(frame)
						}
					}
					rebuilt = append(rebuilt, frame.Data...)
				}
			}
			if count != 1 || string(rebuilt) != input {
				t.Fatal(shortcut, split, count, string(rebuilt))
			}
		}
	}
}

func TestTextPasteNeverReadsClipboard(t *testing.T) {
	e := InteractionEncoder{ImagePaste: true}
	for _, data := range []string{"\x1b[200~", "text\x16\x1b[118;5u", "\x1b[201~"} {
		for _, frame := range e.Frames([]byte(data)) {
			if frame.Type != "input" {
				t.Fatal(frame)
			}
		}
	}
}
