package launcher

import (
	"fennec/internal/terminalstream"
	"testing"
	"time"
)

func TestFramesCarrySequenceAndTypingGap(t *testing.T) {
	interaction := terminalstream.InteractionEncoder{ImagePaste: true}
	data := []byte("ab\x1b[Icd\x16e")
	frames := framesWithSequence(interaction.Frames(data), inputStamp{seq: 100, gap: 30 * time.Millisecond}, true, false)
	var ends []uint64
	for _, frame := range frames {
		ends = append(ends, frame.Seq)
	}
	// "ab" ends at 102, the focus report at 105, "cd" at 107, ^V at 108, "e" at 109.
	want := []uint64{102, 105, 107, 108, 109}
	if len(ends) != len(want) {
		t.Fatalf("frames %+v", frames)
	}
	for i := range want {
		if ends[i] != want[i] {
			t.Fatalf("sequence ends %v, want %v", ends, want)
		}
	}
	if frames[0].DelayMS != 30 {
		t.Fatalf("first keystroke frame delay %d", frames[0].DelayMS)
	}
	for _, frame := range frames[1:] {
		if frame.DelayMS != 0 {
			t.Fatal("only the first frame of a packet carries the typing gap")
		}
	}
	pasted := framesWithSequence(interaction.Frames([]byte("\x1b[200~x\x1b[201~")), inputStamp{seq: 0, gap: time.Second}, true, true)
	if pasted[0].DelayMS != 0 {
		t.Fatal("pastes are written immediately")
	}
	if off := framesWithSequence(interaction.Frames([]byte("x")), inputStamp{gap: time.Second}, false, false); off[0].DelayMS != 0 {
		t.Fatal("no gap without the cadence capability")
	}
}
