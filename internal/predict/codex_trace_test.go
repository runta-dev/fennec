package predict

import "testing"

// TestCodexTracePrediction replays a real Codex session (recorded through
// fen with FEN_PREDICT_TRACE): the prediction for the first typed key must
// render exactly what Codex drew, including the block moving down when the
// "? for shortcuts" row disappears.
func TestCodexTracePrediction(t *testing.T) {
	chunks := loadTrace(t, "codex-100x30")
	s := NewScreen(30, 100)
	p := newCodexProfile()
	var before Lock
	var beforeScreen *Screen
	for _, c := range chunks {
		s.Write(c.Data)
		if !s.Ground() {
			continue
		}
		lock, ok := p.Locate(s)
		if !ok {
			continue
		}
		if lock.State.Empty() {
			before, beforeScreen = lock, s.Clone()
			continue
		}
		if beforeScreen == nil {
			t.Fatal("no empty composer before typing")
		}
		p.Locate(beforeScreen)
		edit := p.Edit(Key{Kind: KeyText, Text: "h"}, before.State, EditContext{})
		if edit == nil {
			t.Fatal("typing into an empty Codex composer was not predicted")
		}
		st := edit(before.State)
		if !p.Same(st, lock.State, s) {
			t.Fatalf("predicted %s, Codex showed %s", describe(st), describe(lock.State))
		}
		paint, ok := p.Render(before, st, beforeScreen)
		if !ok {
			t.Fatal("prediction not renderable")
		}
		if diff := paintDiff(paint, s); diff != "" {
			t.Fatalf("painted rows differ from Codex's frame:\n%s", diff)
		}
		return
	}
	t.Fatal("trace has no typed state")
}
