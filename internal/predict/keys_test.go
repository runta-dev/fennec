package predict

import "testing"

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
	var kinds []KeyKind
	for _, chunk := range []string{"a\x1b[200~line one\n", "line two\n", "three\x1b[201~b"} {
		for _, k := range p.Keys([]byte(chunk)) {
			kinds = append(kinds, k.Kind)
		}
	}
	want := []KeyKind{KeyText, KeyPasteFragment, KeyPasteFragment, KeyPasteFragment, KeyText}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v, want %v", kinds, want)
		}
	}
}
