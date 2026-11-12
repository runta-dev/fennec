//go:build darwin && axlive

// Read-only checks against the real desktop. They send no input. Run with
// -tags axlive in a terminal that has the Accessibility permission.
package desk

import (
	"context"
	"testing"
	"time"

	"fennec/cua/internal/view"
)

func TestLiveWindowsAndTree(t *testing.T) {
	d := New()
	ctx := context.Background()
	start := time.Now()
	wins, err := d.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d windows in %s", len(wins), time.Since(start))
	var front *view.RawWindow
	for i, w := range wins {
		t.Logf("  %s %s %q app=%q pid=%d active=%v bounds=%v owner=%s", w.Key, w.Role, w.Title, w.App, w.PID, w.Active, w.Bounds, w.OwnerKey)
		if front == nil && w.Role != "sheet" {
			front = &wins[i]
		}
	}
	if front == nil {
		t.Skip("no window")
	}
	start = time.Now()
	tree, err := d.Tree(ctx, *front, view.TreeOptions{Menus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tree of %q: %d nodes, truncated=%v, in %s", front.Title, len(tree.Nodes), tree.Truncated, time.Since(start))
	for i, n := range tree.Nodes {
		if i >= 25 {
			break
		}
		t.Logf("  %*s%s %q states=%v bounds=%v keys=%s", n.Depth*2, "", n.Role, n.Name, n.States, n.Bounds, n.Keys)
	}
}
