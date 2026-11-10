package terminalstream

import (
	"bytes"
	"testing"
)

func TestDisplayCollectorPreservesTypedInputAndFragmentedReplies(t *testing.T) {
	var collector DisplayCollector
	input := []byte("typed\x1b]10;rgb:ffff/8080/0000\x1b\\\x1b[6;16;8t\x1b[?1;2c\x03tail")
	for _, b := range input {
		collector.Feed([]byte{b})
	}
	if !collector.Done || collector.Facts.Foreground != "#ff8000" || collector.Facts.CellWidth != 8 || collector.Facts.CellHeight != 16 {
		t.Fatal(collector.Facts, collector.Done)
	}
	if string(collector.Input()) != "typed\x03tail" {
		t.Fatalf("lost typed input: %q", collector.Input())
	}
}
func TestDisplayResponderAnswersOnlyInitialExactInquiry(t *testing.T) {
	facts := DisplayFacts{Foreground: "#ffffff", Background: "#181818", CellWidth: 8, CellHeight: 16}
	responder := DisplayResponder{Facts: &facts}
	var output, replies bytes.Buffer
	refunded := 0
	for _, b := range []byte(DisplayQuery + "READY") {
		out, reply, n := responder.Filter([]byte{b})
		output.Write(out)
		replies.Write(reply)
		refunded += n
	}
	if output.String() != "READY" || refunded != len(DisplayQuery) || !bytes.Contains(replies.Bytes(), []byte("\x1b[?1;2c")) {
		t.Fatal(output.String(), refunded, replies.String())
	}
	out, reply, n := responder.Filter([]byte(DisplayQuery))
	if string(out) != DisplayQuery || len(reply) != 0 || n != 0 {
		t.Fatal("consumed later terminal output")
	}
	for _, input := range []string{"plain output", "\x1b]10;?\a", "\x1b[31mred"} {
		responder := DisplayResponder{Facts: &facts}
		var output bytes.Buffer
		for _, b := range []byte(input) {
			data, reply, n := responder.Filter([]byte{b})
			if len(reply) != 0 || n != 0 {
				t.Fatal("answered a different query")
			}
			output.Write(data)
		}
		output.Write(responder.Flush())
		if output.String() != input {
			t.Fatalf("changed output: %q", output.String())
		}
	}
}
func TestDisplayRepliesRejectUntrustedEscapePayloads(t *testing.T) {
	reply := (DisplayFacts{Foreground: "#ffffff\nBAD", CellWidth: 90000, CellHeight: 20}).Replies()
	if string(reply) != "\x1b[?1;2c" {
		t.Fatalf("invalid facts entered terminal: %q", reply)
	}
}
