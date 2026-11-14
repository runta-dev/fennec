package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// A number or another non-object for a target says what a target is,
// instead of Go's unmarshal error about an internal type.
func TestTargetWrongShape(t *testing.T) {
	var w Expect
	err := json.Unmarshal([]byte(`{"reads": 1}`), &w)
	if err == nil || !strings.Contains(err.Error(), "a target is a ref") || strings.Contains(err.Error(), "plain") {
		t.Fatalf("error %v", err)
	}
	var tg Target
	if err := json.Unmarshal([]byte(`"e12"`), &tg); err != nil || tg.Ref != "e12" {
		t.Fatalf("string shorthand: %+v %v", tg, err)
	}
	if err := json.Unmarshal([]byte(`{"select":"button \"Save\""}`), &tg); err != nil || tg.Select == "" {
		t.Fatalf("object: %+v %v", tg, err)
	}
}
