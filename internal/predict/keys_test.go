package predict

import (
	"strings"
	"testing"
)

func TestParseKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []Key
	}{
		{"hi你", []Key{{Kind: KeyText, Text: "hi你", Bytes: 5}}},
		{"\x1b[13;2u", []Key{{Kind: KeyNewline, Bytes: 7}}},
		{"\x1b\r", []Key{{Kind: KeyNewline, Bytes: 2}}},
		{"\n", []Key{{Kind: KeyNewline, Bytes: 1}}},
		{"\r", []Key{{Kind: KeyEnter, Bytes: 1}}},
		{"\x7f", []Key{{Kind: KeyBackspace, Bytes: 1}}},
		{"\x1b[3~", []Key{{Kind: KeyDelete, Bytes: 4}}},
		{"\x1b[D\x1b[C", []Key{{Kind: KeyLeft, Bytes: 3}, {Kind: KeyRight, Bytes: 3}}},
		{"\x1b[<35;10;5M", []Key{{Kind: KeyNeutral, Bytes: 11}}},
		{"\x1b[I", []Key{{Kind: KeyNeutral, Bytes: 3}}},
		{"\x1b[97;1:3u", []Key{{Kind: KeyNeutral, Bytes: 9}}},
		{"\x1b[97u", []Key{{Kind: KeyText, Text: "a", Bytes: 5}}},
		{"\x1b[97:65;2u", []Key{{Kind: KeyText, Text: "A", Bytes: 10}}},
		{"\x1b[118;5u", []Key{{Kind: KeyImagePaste, Bytes: 8}}},
		{"\x16", []Key{{Kind: KeyImagePaste, Bytes: 1}}},
		{"\x1b[127;3u", []Key{{Kind: KeyAltBackspace, Bytes: 8}}},
		{"\x1b[200~a\nb\x1b[201~x", []Key{{Kind: KeyPaste, Text: "a\nb", Bytes: 15}, {Kind: KeyText, Text: "x", Bytes: 1}}},
		{"\x01", []Key{{Kind: KeyCtrl, Rune: 'a', Bytes: 1}}},
		{"\x1b[97;5u", []Key{{Kind: KeyCtrl, Rune: 'a', Bytes: 7}}},
		{"\x1b", []Key{{Kind: KeyEscape, Bytes: 1}}},
	}
	for _, tc := range cases {
		got := ParseKeys([]byte(tc.in))
		if len(got) != len(tc.want) {
			t.Fatalf("%q: got %+v", tc.in, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q: key %d = %+v, want %+v", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestPasteSplitAcrossReads(t *testing.T) {
	var p PasteSplitter
	var keys []Key
	for _, chunk := range []string{"a\x1b[200~line one\n", "line two\n", "three\x1b[201~b"} {
		keys = append(keys, p.Keys([]byte(chunk))...)
	}
	want := []KeyKind{KeyText, KeyNeutral, KeyNeutral, KeyPaste, KeyText}
	if len(keys) != len(want) {
		t.Fatalf("keys %+v, want kinds %v", keys, want)
	}
	for i := range want {
		if keys[i].Kind != want[i] {
			t.Fatalf("keys %+v, want kinds %v", keys, want)
		}
	}
	if keys[3].Text != "line one\nline two\nthree" {
		t.Fatalf("paste %q", keys[3].Text)
	}
	bytes := 0
	for _, k := range keys {
		bytes += k.Bytes
	}
	if bytes != len("a\x1b[200~line one\n")+len("line two\n")+len("three\x1b[201~b") {
		t.Fatalf("keys cover %d bytes", bytes)
	}
}

// TestPasteSplitOverflow: a paste too large to hold is not predicted.
func TestPasteSplitOverflow(t *testing.T) {
	var p PasteSplitter
	p.Keys([]byte("\x1b[200~"))
	big := strings.Repeat("x", maxSplitPaste)
	p.Keys([]byte(big))
	keys := p.Keys([]byte("y\x1b[201~"))
	if len(keys) != 1 || keys[0].Kind != KeyPasteFragment {
		t.Fatalf("keys %+v", keys)
	}
	if keys = p.Keys([]byte("\x1b[200~ok\x1b[201~")); len(keys) != 1 || keys[0].Kind != KeyPaste || keys[0].Text != "ok" {
		t.Fatalf("next paste %+v", keys)
	}
}
