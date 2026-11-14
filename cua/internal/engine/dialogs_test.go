package engine_test

import (
	"strings"
	"testing"

	"fennec/cua/internal/protocol"
)

// A dialog another process raised (osascript's display dialog) shows in the
// inventory before the installed apps, so an agent notices it, and the
// inventory keeps the reference's shape.
func TestInventoryListsForeignDialogFirst(t *testing.T) {
	w := newWorld()
	w.windows = append([]protocol.Window{{Ref: "w2", App: "osascript", Title: "Keyboard Settings", Role: "dialog", PID: 77, Active: true}}, w.windows...)
	c := session(t, w.engine())
	out := c.ok(`await cua.getState()`)
	want := `{"apps":[{"displayName":"osascript","id":"osascript","isRunning":true},` +
		`{"displayName":"Runta CUA Fixture","id":"dev.runta.fixture","isRunning":true},` +
		`{"displayName":"TextEdit","id":"com.apple.TextEdit","isRunning":false,"lastUsedDate":815184000,"useCount":3}],"browsers":[]}`
	if out != want {
		t.Fatalf("state %s\nwant  %s", out, want)
	}
}

// fennec.observe shows its result like the cua reads, and keeps quiet with
// emit: false; an image is shown as an image, not as base64 text.
func TestFennecObserveShowsResult(t *testing.T) {
	w := newWorld()
	w.windows = append(w.windows, protocol.Window{Ref: "w2", App: "osascript", Title: "Keyboard Settings", Role: "dialog", PID: 77})
	c := session(t, w.engine())
	out := c.ok(`await fennec.observe({ scope: "desktop" })`)
	if !strings.Contains(out, `"title":"Keyboard Settings"`) || !strings.Contains(out, `"app":"osascript"`) {
		t.Errorf("desktop observe shows %q", out)
	}
	if out := c.ok(`var o = await fennec.observe({ scope: "desktop", emit: false }); nodeRepl.write(String(o.windows.length))`); out != "2" {
		t.Errorf("quiet observe shows %q", out)
	}
	r := c.js(`await fennec.observe({ window: "w1", image: "window" })`)
	if r.isError {
		t.Fatalf("image observe: %s", r.all())
	}
	images := 0
	for _, b := range r.blocks {
		if b.Type == "image" {
			images++
		}
	}
	if images != 1 || strings.Contains(r.output(), `"data"`) {
		t.Errorf("image observe: %d images, text %q", images, r.output())
	}
}
