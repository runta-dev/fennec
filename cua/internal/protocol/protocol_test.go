package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTargetShorthand(t *testing.T) {
	cases := map[string]Target{
		`"e12"`:                         {Ref: "e12"},
		`"w3"`:                          {Ref: "w3"},
		`"button \"Save\""`:             {Select: `button "Save"`},
		`{"point":{"x":10,"y":20}}`:     {Point: &Point{X: 10, Y: 20}},
		`{"select":"OK","window":"w2"}`: {Select: "OK", Window: "w2"},
	}
	for in, want := range cases {
		var got Target
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Errorf("%s: got %s, want %s", in, gj, wj)
		}
	}
}

func TestTargetRejectsTwoKinds(t *testing.T) {
	for _, in := range []string{
		`{"ref":"e1","select":"OK"}`,
		`{}`,
		`{"ref":"button"}`,
		`{"point":{"x":1,"y":2,"space":"moon"}}`,
	} {
		var got Target
		if err := json.Unmarshal([]byte(in), &got); err == nil {
			t.Errorf("%s: accepted", in)
		}
	}
}

func TestReadmeActExampleDecodes(t *testing.T) {
	in := `{"steps": [
	  {"set": "textbox \"Name\"", "value": "Alice"},
	  {"press": "button \"Save\"", "expect": {"window_gone": "Save As"}}
	]}`
	var req ActRequest
	dec := json.NewDecoder(strings.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatal(err)
	}
	if len(req.Steps) != 2 {
		t.Fatalf("steps = %d", len(req.Steps))
	}
	for i, want := range []string{"set", "press"} {
		got, err := req.Steps[i].Kind()
		if err != nil || got != want {
			t.Errorf("step %d kind = %q, %v; want %q", i, got, err, want)
		}
	}
	if req.Steps[1].Expect == nil || req.Steps[1].Expect.WindowGone != "Save As" {
		t.Errorf("expect not decoded: %+v", req.Steps[1].Expect)
	}
}

func TestStepKindRequiresExactlyOne(t *testing.T) {
	text := "hi"
	if _, err := (Step{Key: "cmd+s", Type: &text}).Kind(); err == nil {
		t.Error("two actions accepted")
	}
	if _, err := (Step{}).Kind(); err == nil {
		t.Error("no action accepted")
	}
}
