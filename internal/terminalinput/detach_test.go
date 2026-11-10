package terminalinput

import (
	"bytes"
	"testing"
)

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
