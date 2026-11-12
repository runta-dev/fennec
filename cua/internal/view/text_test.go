package view

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestNameNormalisation(t *testing.T) {
	for in, want := range map[string]string{
		"  _Export As...  ":   "export as",
		"Font size:":          "font size",
		"Line\t spacing  …":   "line spacing",
		"_Open":               "open",
		"Name:  ":             "name",
		"Plain":               "plain",
		"___":                 "",
		"...":                 "",
		"Re:  Hello   World:": "re: hello world",
	} {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuoteEscapesAndCuts(t *testing.T) {
	got, cut := Quote("say \"hi\"\nback\\slash", 80)
	if got != `say \"hi\"\nback\\slash` || cut {
		t.Errorf("Quote escapes: %q cut=%v", got, cut)
	}
	// Characters, not bytes.
	if got, cut := Quote("naïve café", 4); got != "naïv" || !cut {
		t.Errorf("Quote cut by characters: %q %v", got, cut)
	}
	if got, cut := Quote("€éü", 3); got != "€éü" || cut {
		t.Errorf("Quote at exactly max: %q %v", got, cut)
	}
	if got := Field(strings.Repeat("a", 90), NameChars); got != `"`+strings.Repeat("a", 80)+`"…` {
		t.Errorf("Field marks a cut: %q", got)
	}
	if got, _ := Quote("a\rb\tc\u2028d\x00", 80); got != `a\rb\tc\u2028d\u0000` {
		t.Errorf("Quote escapes controls and separators: %q", got)
	}
	if got := Clip("naïve", 3, false); got != "naï" {
		t.Errorf("Clip head: %q", got)
	}
	if got := Clip("ab", 5, false); got != "ab" {
		t.Errorf("Clip short: %q", got)
	}
}

func TestTailClipWholeCharacters(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		tail bool
		want string
	}{
		{"xy€🎉", 2, true, "€🎉"},
		{"xy€🎉", 3, true, "y€🎉"},
		{"€🎉", 1, true, "🎉"},
		{"€🎉", 2, true, "€🎉"},
		{"€🎉", 9, true, "€🎉"},
		{"€🎉", 0, true, ""},
		{"€🎉", 1, false, "€"},
		{"👩‍👧", 1, true, "👧"}, // a code point, never a split byte
	} {
		got := Clip(c.in, c.max, c.tail)
		if got != c.want {
			t.Errorf("Clip(%q, %d, %v) = %q, want %q", c.in, c.max, c.tail, got, c.want)
		}
		if !strings.HasSuffix(c.in, got) && c.tail {
			t.Errorf("tail clip %q is not a suffix of %q", got, c.in)
		}
	}
}

func TestWholeNumberFormatting(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{1, "1"}, {0, "0"}, {math.Copysign(0, -1), "0"}, {-3, "-3"}, {2.5, "2.5"},
		{100000, "100000"}, {0.1, "0.1"}, {1e20, "1e+20"},
	} {
		if got := FormatNumber(c.in); got != c.want {
			t.Errorf("FormatNumber(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if v := ValueString(float64(7)); v == nil || *v != "7" {
		t.Errorf("ValueString(7.0) = %v", v)
	}
	if v := ValueString(math.Copysign(0, -1)); v == nil || *v != "0" {
		t.Errorf("ValueString(-0) = %v", v)
	}
	if v := ValueString(nil); v != nil {
		t.Errorf("ValueString(nil) = %v, want nil", *v)
	}
	// A whole value in a node prints as such.
	n := NodeOf(RawNode{Role: "slider", Value: ValueString(42.0)}, "e1", "", false)
	b, _ := json.Marshal(n)
	if !strings.Contains(string(b), `"value":"42"`) {
		t.Errorf("node value: %s", b)
	}
}

func TestAppNameMatching(t *testing.T) {
	yes := [][2]string{
		{"Chrome", "Google Chrome"},
		{"chrome", "Google Chrome"},
		{"Text", "TextEdit"},
		{"Notepad++", "Notepad++"},
		{"notepad++", "Notepad++"},
		{"Google Chrome", "Google Chrome"},
		{"Settings", "System Settings"},
	}
	no := [][2]string{
		{"rom", "Google Chrome"}, // a short inner fragment
		{"", "TextEdit"},         // empty
		{"   ", "TextEdit"},      // blank
		{"notepad", "Notepad++"}, // a different suffix
		{"Notepad++", "Notepad"}, // the plus belongs to the word
		{"Safari", "Google Chrome"},
	}
	for _, c := range yes {
		if !AppMatches(c[0], c[1]) {
			t.Errorf("%q should name %q", c[0], c[1])
		}
	}
	for _, c := range no {
		if AppMatches(c[0], c[1]) {
			t.Errorf("%q should not name %q", c[0], c[1])
		}
	}
}

func TestAppNameWordMatch(t *testing.T) {
	cases := []struct {
		want, app string
		ok        bool
	}{
		{"Note", "Notes", true},    // begins a word, at least three letters
		{"Note", "Notepad", true},  // begins a word
		{"Text", "TextEdit", true}, // begins a word
		{"Chrome", "Google Chrome", true},
		{"Edit", "TextEdit", true},  // ends a word with four letters
		{"e", "TextEdit", false},    // too short
		{"pad", "Notepad", false},   // ends a word with only three letters
		{"No", "Notes", false},      // begins with only two letters
		{"xtEd", "TextEdit", false}, // inner fragment
		{"notepad", "notepad++", false},
		{"notepad++", "notepad++", true},
		{"Visual Code", "Visual Studio Code", true}, // every word matches some word
		{"Visual Excel", "Visual Studio Code", false},
	}
	for _, c := range cases {
		if got := AppMatches(c.want, c.app); got != c.ok {
			t.Errorf("AppMatches(%q, %q) = %v, want %v", c.want, c.app, got, c.ok)
		}
	}
}

func TestAppMatchWholeWord(t *testing.T) {
	ws := []RawWindow{
		{Key: "1", App: "Google Chrome", PID: 1, Title: "Gmail - Inbox", Active: true},
		{Key: "2", App: "Notepad++", PID: 2, Title: "notes.txt"},
		{Key: "3", App: "Mail", PID: 3, Title: "Inbox"},
	}
	// The app name is matched first: Mail is the Mail app, not Gmail's tab.
	w, err := PickApp(ws, "Mail")
	if err != nil || w.Key != "3" {
		t.Fatalf("Mail picked %+v, %v", w, err)
	}
	// Without the Mail app, titles match only by whole words.
	if got := AppWindows(ws[:2], "Mail"); len(got) != 0 {
		t.Errorf("Mail adopted %+v", got)
	}
	if got := AppWindows(ws[:2], "Gmail"); len(got) != 1 || got[0].Key != "1" {
		t.Errorf("Gmail by title word: %+v", got)
	}
	// A plus belongs to its word.
	if got := AppWindows(ws, "notepad"); len(got) != 0 {
		t.Errorf("notepad named notepad++: %+v", got)
	}
	if w, err := PickApp(ws, "notepad++"); err != nil || w.Key != "2" {
		t.Errorf("notepad++: %+v %v", w, err)
	}
	if !TitleHasWords("Re: Mail merge", "mail merge") || TitleHasWords("Gmail - Inbox", "mail") {
		t.Error("TitleHasWords")
	}
	if _, err := PickApp(ws, "Safari"); code(t, err) != "window_not_found" || !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("unknown app: %v", err)
	}
	if _, err := PickApp(ws, " "); code(t, err) != "invalid_argument" {
		t.Errorf("blank app: %v", err)
	}
}

func TestStripsObjectReplacement(t *testing.T) {
	n := RawNode{Role: "text", Name: "Photo \uFFFCcaption", Value: str("a\uFFFCb")}
	s := snap(t, RawWindow{Key: "k", Title: "Doc"}, []RawNode{n})
	line := RenderOutline(s, OutlineOptions{}).Text
	if strings.ContainsRune(line, '\uFFFC') || !strings.Contains(line, `"Photo caption": "ab"`) {
		t.Errorf("outline keeps U+FFFC: %q", line)
	}
	node := NodeOf(n, "e1", "", false)
	if node.Name != "Photo caption" || *node.Value != "ab" {
		t.Errorf("node keeps U+FFFC: %+v", node)
	}
	if v := VisionLine(1, "text", n.Name, n.Value); strings.ContainsRune(v, '\uFFFC') {
		t.Errorf("vision line keeps U+FFFC: %q", v)
	}
	// The element kept for actions keeps its original name.
	if s.Tree.Nodes[0].Name != "Photo \uFFFCcaption" {
		t.Errorf("snapshot node changed: %q", s.Tree.Nodes[0].Name)
	}
	reg := NewRegistry(0)
	walk := reg.Begin()
	sn := walk.Snapshot(Tree{Window: RawWindow{Key: "k"}, Nodes: []RawNode{n}})
	walk.End()
	e, err := reg.Lookup(sn.Refs[0])
	if err != nil || e.Node.Name != "Photo \uFFFCcaption" {
		t.Errorf("registry node: %+v %v", e.Node, err)
	}
}
