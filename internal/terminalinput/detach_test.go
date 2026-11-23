package terminalinput

import (
	"bytes"
	"strings"
	"testing"
)

func TestStringRepliesStayAtomicAcrossDelayedReads(t *testing.T) {
	for _, reply := range []string{
		"\x1b]10;rgb:cdcd/d6d6/e4e4\x1b\\",
		"\x1b]11;rgb:1010/1616/2020\a",
		"\x1b]12;rgb:ffff/ffff/ffff\x1b\\",
		"\x1bP1+r544e=787465726d\x1b\\",
		"\x1b_Gi=1;OK\x1b\\",
	} {
		for split := 2; split < len(reply); split++ {
			var d Decoder
			out, detached := d.Feed([]byte("before" + reply[:split]))
			if detached || string(out) != "before" {
				t.Fatalf("split %d: forwarded a reply prefix: %q", split, out)
			}
			// The remote attach parser's escape timeout must never see this
			// prefix, even if the next local read arrives after idle polls.
			for range 3 {
				if out := d.Flush(); len(out) != 0 || d.PollTimeout(100) != 100 {
					t.Fatalf("split %d: idle poll released %q", split, out)
				}
			}
			out, detached = d.Feed([]byte(reply[split:] + "after"))
			if detached || string(out) != reply+"after" {
				t.Fatalf("split %d: reply or typing changed: %q", split, out)
			}
		}
	}
}

func TestStringControlsPreservePasteAndBoundBuffer(t *testing.T) {
	input := "\x1b[200~code \x1b]10;rgb:cdcd/d6d6/e4e4\a\x03\x1b[201~"
	var d Decoder
	var output []byte
	for _, b := range []byte(input) {
		out, detached := d.Feed([]byte{b})
		if detached {
			t.Fatal("paste detached the session")
		}
		output = append(output, out...)
	}
	if string(output) != input {
		t.Fatalf("paste changed: %q", output)
	}
	input = "\x1b]" + strings.Repeat("x", 511)
	out, _ := d.Feed([]byte(input))
	if string(out) != input || len(d.pending) != 0 {
		t.Fatal("unterminated string exceeded the buffer limit")
	}
}

func TestDetachKeysNeverReachAgent(t *testing.T) {
	for _, key := range []byte{3, 4, 28} {
		data, yes := Detach([]byte{'a', key, 'z'})
		if !yes || !bytes.Equal(data, []byte{'a', 28}) {
			t.Fatal(data, yes)
		}
	}
}
func TestExplicitCommandsPreserved(t *testing.T) {
	for _, command := range []string{"/exit\r", "/quit\r", "hello\r"} {
		data, yes := Detach([]byte(command))
		if yes || string(data) != command {
			t.Fatal(command)
		}
	}
}

func TestExtendedDetachEverySplit(t *testing.T) {
	for _, sequence := range []string{"\x1b[99;5u", "\x1b[99;5:1u", "\x1b[99;5:2u", "\x1b[67;6u", "\x1b[100;5u", "\x1b[27;5;99~", "\x1b[3u"} {
		for split := 1; split < len(sequence); split++ {
			d := Decoder{}
			first, yes := d.Feed([]byte(sequence[:split]))
			if yes {
				t.Fatal("early detach", sequence, split)
			}
			second, yes := d.Feed([]byte(sequence[split:]))
			if !yes || string(append(first, second...)) != string([]byte{28}) {
				t.Fatal(sequence, split, first, second, yes)
			}
		}
	}
}
func TestEscapePasteAndUnmodifiedKeys(t *testing.T) {
	for _, sequence := range []string{"\x1b[A", "\x1b[99u", "\x1b[99;5:3u", "\x1b[200~hello\x03\x1b[201~", "/quit\r"} {
		d := Decoder{}
		out, yes := d.Feed([]byte(sequence))
		out = append(out, d.Flush()...)
		if yes || string(out) != sequence {
			t.Fatal(sequence, string(out), yes)
		}
	}
	d := Decoder{}
	out, yes := d.Feed([]byte{27})
	if len(out) != 0 || yes {
		t.Fatal(out)
	}
	if string(d.Flush()) != "\x1b" {
		t.Fatal("escape lost")
	}
}

func TestLoneEscapeWaitsBriefly(t *testing.T) {
	var d Decoder
	if d.PollTimeout(100) != 100 {
		t.Fatal("idle decoder must keep the idle timeout")
	}
	if out, _ := d.Feed([]byte{0x1b}); len(out) != 0 {
		t.Fatalf("lone ESC sent before its sequence could complete: %q", out)
	}
	if got := d.PollTimeout(100); got != EscapeWait {
		t.Fatalf("pending ESC waits %d ms, want %d", got, EscapeWait)
	}
	if string(d.Flush()) != "\x1b" || d.PollTimeout(100) != 100 {
		t.Fatal("flush must release the ESC and restore the idle timeout")
	}
	// A sequence split across reads is still joined.
	d.Feed([]byte("\x1b["))
	if out, _ := d.Feed([]byte("A")); string(out) != "\x1b[A" {
		t.Fatalf("split sequence = %q", out)
	}
}
