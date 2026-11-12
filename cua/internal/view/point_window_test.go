package view

import (
	"context"
	"testing"

	"fennec/cua/internal/protocol"
)

// inWindow answers a hit test within one window's app.
type inWindow struct {
	*fakeSource
	in fakeAt
}

func (s inWindow) AtIn(context.Context, RawWindow, int, int) (*RawNode, []RawNode, *RawWindow, *RawWindow, error) {
	return s.in.node, s.in.path, s.in.window, s.in.covered, nil
}

// A point asked of a window that another app's floating window covers
// answers that window's element and names the cover (found live: the
// cover's own element came back, with the asked window as "covered").
func TestPointInAskedWindow(t *testing.T) {
	target := win("t", "Covered Target", 2, false)
	target.Bounds = box(0, 0, 500, 500)
	cover := RawWindow{Key: "cg:9", Title: "Other App Cover", PID: 8, Role: "window"}
	coverEl := RawNode{Handle: &handle{1}, Role: "group", Name: "Other App Pad"}
	padEl := RawNode{Handle: &handle{2}, Role: "group", Name: "Target Pad"}
	src := inWindow{
		fakeSource: &fakeSource{windows: []RawWindow{target}, at: fakeAt{node: &coverEl, window: &cover, covered: &target}},
		in:         fakeAt{node: &padEl, window: &target, covered: &cover},
	}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	req := obsReq{target: &protocol.Target{Point: &protocol.Point{X: 10, Y: 10}}}.req()
	req.Window = "Covered Target"
	res, err := o.Observe(ctx(), req)
	if err != nil || res.At.Window.Title != "Covered Target" || res.At.Element.Name != "Target Pad" || res.At.Covered == nil || res.At.Covered.Title != "Other App Cover" {
		t.Fatalf("%+v %v", res.At, err)
	}
}
