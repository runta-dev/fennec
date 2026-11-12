package view

import (
	"strings"
	"sync"
	"testing"
)

func nodesN(n int) []RawNode {
	specs := make([]spec, n)
	for i := range specs {
		specs[i] = spec{depth: 1, role: "button", name: "b"}
	}
	specs[0] = spec{depth: 0, role: "group"}
	return build(specs...)
}

func TestIDCacheEvictsAtCapKeepingListedWindows(t *testing.T) {
	reg := NewRegistry(10)
	a := RawWindow{Key: "cg:1", Title: "A"}
	b := RawWindow{Key: "cg:2", Title: "B"}

	// Below the cap nothing is evicted, over many walks.
	var small []string
	for i := 0; i < 2; i++ {
		walk := reg.Begin()
		walk.Windows([]RawWindow{a, b})
		s := walk.Snapshot(Tree{Window: a, Nodes: nodesN(3)})
		small = append(small, s.Refs...)
		walk.End()
	}
	if reg.Len() != 8 {
		t.Fatalf("len %d, want 8", reg.Len())
	}
	for _, r := range small {
		if _, err := reg.Lookup(r); err != nil {
			t.Fatalf("%s evicted below the cap: %v", r, err)
		}
	}

	// A walk that crosses the cap keeps every id it hands out.
	walk := reg.Begin()
	wrefs := walk.Windows([]RawWindow{a, b})
	big := walk.Snapshot(Tree{Window: b, Nodes: nodesN(6)})
	if reg.Len() <= 10 {
		t.Fatalf("len %d should be over the cap", reg.Len())
	}
	for _, r := range append(append([]string{}, small...), big.Refs...) {
		if _, err := reg.Lookup(r); err != nil {
			t.Fatalf("%s lost during the walk that crossed the cap: %v", r, err)
		}
	}
	walk.End()

	// The next walk keeps only the listed windows' ids.
	walk = reg.Begin()
	if reg.Len() != 2 {
		t.Fatalf("after trim len %d, want the 2 listed windows", reg.Len())
	}
	for _, r := range wrefs {
		e, err := reg.Lookup(r)
		if err != nil || !e.Window {
			t.Fatalf("window ref %s lost: %v", r, err)
		}
	}
	again := walk.Windows([]RawWindow{a, b})
	walk.End()
	if again[0] != wrefs[0] || again[1] != wrefs[1] {
		t.Errorf("window refs changed: %v -> %v", wrefs, again)
	}
	_, err := reg.Lookup(big.Refs[1])
	if code(t, err) != "stale_ref" || !strings.Contains(hint(err), "observe") {
		t.Errorf("stale element ref: %v (hint %q)", err, hint(err))
	}
	// A window no longer listed loses its ref at a trim too.
	walk = reg.Begin()
	walk.Windows([]RawWindow{a})
	walk.Snapshot(Tree{Window: a, Nodes: nodesN(12)})
	walk.End()
	reg.Begin().End()
	if _, err := reg.Lookup(wrefs[1]); code(t, err) != "stale_ref" {
		t.Errorf("unlisted window survived the trim: %v", err)
	}
	if _, err := reg.Lookup(wrefs[0]); err != nil {
		t.Errorf("listed window lost: %v", err)
	}
}

func TestRefsSurviveIdCapEviction(t *testing.T) {
	reg := NewRegistry(5)
	w := RawWindow{Key: "cg:9", Title: "Doc"}
	walk := reg.Begin()
	walk.Windows([]RawWindow{w})
	s := walk.Snapshot(Tree{Window: w, Nodes: nodesN(8)})
	walk.End()

	// An action presses a ref from the outline that crossed the cap.
	release := reg.Hold()
	e, err := reg.Lookup(s.Refs[7])
	if err != nil || e.Handle != s.Tree.Nodes[7].Handle || e.WindowRef != s.WindowRef {
		t.Fatalf("ref of the snapshot that crossed the cap: %+v %v", e, err)
	}
	// A read that starts while the action holds the table does not trim it.
	reg.Begin().End()
	if _, err := reg.Lookup(s.Refs[7]); err != nil {
		t.Fatalf("trimmed during an action: %v", err)
	}
	release()
	release() // releasing twice is harmless

	// Re-observing evicts the stale ids; only current ones are held.
	walk = reg.Begin()
	walk.Windows([]RawWindow{w})
	fresh := walk.Snapshot(Tree{Window: w, Nodes: nodesN(2)})
	walk.End()
	if _, err := reg.Lookup(s.Refs[7]); code(t, err) != "stale_ref" {
		t.Errorf("old ref after re-observe: %v", err)
	}
	if reg.Len() != 3 {
		t.Errorf("held %d refs, want the window and 2 elements", reg.Len())
	}
	for _, r := range fresh.Refs {
		if _, err := reg.Lookup(r); err != nil {
			t.Errorf("fresh ref %s: %v", r, err)
		}
	}
}

func TestRefTableTrimBoundaries(t *testing.T) {
	reg := NewRegistry(4)
	w := RawWindow{Key: "cg:1"}
	first := reg.Begin()
	first.Windows([]RawWindow{w})
	s := first.Snapshot(Tree{Window: w, Nodes: nodesN(6)})

	// A find that starts while the snapshot is still running does not trim.
	find := reg.Begin()
	for _, r := range s.Refs {
		if _, err := reg.Lookup(r); err != nil {
			t.Fatalf("ref %s trimmed in the middle of a snapshot: %v", r, err)
		}
	}
	find.End()
	first.End()
	first.End() // ending twice is harmless

	// Lookups never trim.
	for i := 0; i < 3; i++ {
		if _, err := reg.Lookup(s.Refs[5]); err != nil {
			t.Fatalf("lookup trimmed: %v", err)
		}
	}
	// The next snapshot, find or hit test trims at its start.
	reg.Begin().End()
	if _, err := reg.Lookup(s.Refs[5]); code(t, err) != "stale_ref" {
		t.Errorf("not trimmed at the next walk: %v", err)
	}

	// Malformed refs are invalid, kinds are checked.
	if _, err := reg.Lookup("x1"); code(t, err) != "invalid_argument" {
		t.Errorf("malformed: %v", err)
	}
	if _, err := reg.Lookup("e" + s.WindowRef[1:]); code(t, err) != "stale_ref" {
		t.Errorf("a window number as an element ref: %v", err)
	}

	// Concurrent walks and lookups are safe.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			walk := reg.Begin()
			walk.Windows([]RawWindow{w})
			sn := walk.Snapshot(Tree{Window: w, Nodes: nodesN(3)})
			for _, r := range sn.Refs {
				if _, err := reg.Lookup(r); err != nil {
					t.Errorf("concurrent walk lost %s: %v", r, err)
				}
			}
			walk.End()
		}()
	}
	wg.Wait()
}

func TestElementRefsStableByKey(t *testing.T) {
	reg := NewRegistry(0)
	w := RawWindow{Key: "cg:1"}
	n := RawNode{Key: "btn", Role: "button", Name: "OK"}
	a := reg.Begin()
	r1 := a.Snapshot(Tree{Window: w, Nodes: []RawNode{n}}).Refs[0]
	a.End()
	b := reg.Begin()
	r2 := b.Snapshot(Tree{Window: w, Nodes: []RawNode{n}}).Refs[0]
	b.End()
	if r1 != r2 {
		t.Errorf("keyed element got %s then %s", r1, r2)
	}
	if reg.View() != "v2" || b.View() != "v2" {
		t.Errorf("view %s", reg.View())
	}
}
