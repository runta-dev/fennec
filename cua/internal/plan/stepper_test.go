package plan

import (
	"errors"
	"strconv"
	"testing"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
)

// A slider that refuses a written value is walked by its decrement action
// (found live: setting the fixture's Volume to 30 failed with -25201).
func TestSliderSteppedWhenWriteRefused(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	v := "50"
	r.d.Add("cg:1", "vol", "slider", "Volume", nil, "cg:1")
	r.d.Node("vol").Value = &v
	r.d.SetErr = errors.New("AXValue: accessibility error -25201")
	r.d.onPerform = func(id, action string) {
		if id != "vol" {
			return
		}
		n := r.d.Node("vol")
		f, _ := strconv.Atoi(*n.Value)
		switch action {
		case "AXDecrement":
			f -= 10
		case "AXIncrement":
			f += 10
		}
		s := strconv.Itoa(f)
		n.Value = &s
	}
	res := r.run(protocol.Step{Set: sel(`slider "Volume"`), Value: raw("30")})
	mustPass(t, res)
	if *r.d.Node("vol").Value != "30" {
		t.Fatal(*r.d.Node("vol").Value)
	}
}
