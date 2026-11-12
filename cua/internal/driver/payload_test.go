package driver

import (
	"encoding/json"
	"testing"
)

// The real driver answers MCP-style, with its fields in structuredContent;
// readers that decoded the top level saw none of them (found live: every
// hit-checked click was refused as "not in the driver's snapshot").
func TestPayload(t *testing.T) {
	wrapped := json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"structuredContent":{"effect":"confirmed","elements":[{"element_token":"s1:1"}]}}`)
	var r struct {
		Effect   string `json:"effect"`
		Elements []any  `json:"elements"`
	}
	if err := json.Unmarshal(Payload(wrapped), &r); err != nil || r.Effect != "confirmed" || len(r.Elements) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	flat := json.RawMessage(`{"effect":"partial"}`)
	if string(Payload(flat)) != string(flat) {
		t.Fatal(string(Payload(flat)))
	}
}
