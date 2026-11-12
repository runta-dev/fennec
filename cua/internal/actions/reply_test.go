package actions

import (
	"encoding/json"
	"testing"
)

// A driver reply's fields sit in structuredContent.
func TestParseReplyStructured(t *testing.T) {
	r := parseReply(json.RawMessage(`{"content":[{"type":"text","text":"typed"}],"structuredContent":{"effect":"partial","delivered_chars":2,"terminal":true}}`))
	if r.Effect != "partial" || r.DeliveredChars == nil || *r.DeliveredChars != 2 || !r.Terminal {
		t.Fatalf("%+v", r)
	}
}

// A web password field reads back as bullets (found live in Safari: a
// typed password was reported "not shown" and its Return withheld).
func TestMasked(t *testing.T) {
	if !masked("••••") || masked("") || masked("pw-9") {
		t.Fatal("masked")
	}
}
