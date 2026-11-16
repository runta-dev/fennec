package terminalstream

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"fennec/internal/predict"
)

// finalStates folds the state assignments of a stream: the last token of
// each key, in the order of their last occurrence.
func finalStates(stream []byte) []stateToken {
	var z safetyScanner
	var states []stateToken
	z.feed(stream, func(token []byte, safe bool) {
		if key := stateKey(token); !safe && key != "" {
			states = addState(states, stateToken{key, append([]byte(nil), token...)})
		}
	})
	return states
}

// sameStates: the client is left with the same state assignments, in the
// same order where it matters.
func sameStates(read, sent []byte) bool {
	return slices.EqualFunc(finalStates(read), finalStates(sent), func(a, b stateToken) bool {
		return a.key == b.key && bytes.Equal(a.token, b.token)
	})
}

func TestStateKey(t *testing.T) {
	for token, want := range map[string]string{
		"\x1b[2 q":                       "cursor-style",
		"\x1b[ q":                        "cursor-style",
		"\x1b]0;⠹ Count 1 to 600\x07":    "osc0",
		"\x1b]2;title\x1b\\":             "osc2",
		"\x1b]12;#eeeeee\x07":            "osc12",
		"\x1b]22;default\x1b\\":          "osc22",
		"\x1b]112\x07":                   "osc112",
		"\x1b_Ga=d,d=I,i=1,q=2\x1b\\":    "kitty Ga=d,d=I,i=1,q=2",
		"\x1b_Ga=d,d=A,q=2\x1b\\":        "kitty Ga=d,d=A,q=2",
		"\x1b]11;?\x07":                  "", // a query: the terminal replies
		"\x1b]10;red;blue\x07":           "", // sets two colours
		"\x1b]52;c;aGk=\x07":             "", // clipboard
		"\x1b]7501;state=idle\x1b\\":     "",
		"\x1b]0;bell\x07in\x07":          "", // control character inside
		"\x1b_Ga=d,d=I,i=1\x1b\\":        "", // may reply
		"\x1b_Ga=T,f=100,q=2;AAAA\x1b\\": "",
		"\x1b[?1000h":                    "",
		"\x1b[>5u":                       "",
		"\x1b[!p":                        "",
		"\x1b[12q":                       "",
	} {
		if got := stateKey([]byte(token)); got != want {
			t.Errorf("%q: key %q, want %q", token, got, want)
		}
	}
}

// TestConflatorMergesStates: frames carrying a spinner title, cursor style,
// colours and kitty deletions (as Codex, OpenCode, Grok and Pi draw them)
// merge; the client ends on the same screen with the last of each state,
// and a reply-producing query between frames is passed on in its place.
func TestConflatorMergesStates(t *testing.T) {
	c := newConflator(30, 100)
	reference := predict.NewScreen(30, 100)
	client := predict.NewScreen(30, 100)
	start := "\x1b[?1049h\x1b[2J"
	c.Write([]byte(start))
	reference.Write([]byte(start))
	client.Write(c.Take())
	var read []byte
	spinner := []string{"⠋", "⠙", "⠹", "⠸"}
	for step := 0; step < 40; step++ {
		frame := codexLikeStep(500-3*step) +
			fmt.Sprintf("\x1b]0;%s Working\x07\x1b[%d q\x1b[59m\x1b_Ga=d,d=I,i=1,q=2\x1b\\", spinner[step%4], 1+step%2)
		if step == 7 {
			frame += "\x1b]12;#eeeeee\x07\x1b]22;default\x1b\\"
		}
		if step == 20 {
			frame += "\x1b]11;?\x07"
		}
		c.Write([]byte(frame))
		reference.Write([]byte(frame))
		read = append(read, frame...)
	}
	out := c.Take()
	client.Write(out)
	if same, why := predict.SameScreen(client, reference); !same {
		t.Fatal(why)
	}
	if !sameStates(read, out) {
		t.Fatalf("states differ:\nread %q\nsent %q", finalStates(read), finalStates(out))
	}
	if strings.Count(string(out), "\x1b]11;?\x07") != 1 {
		t.Fatal("the query was not passed on once")
	}
	if merged, _, _ := c.Stats(); merged < 38 || len(out)*5 > len(read) {
		t.Fatalf("%d frames merged, %d of %d bytes sent", merged, len(out), len(read))
	}
}
