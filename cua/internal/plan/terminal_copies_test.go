package plan

import (
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
)

// A command already in scrollback that lands only in part is not shown,
// even when the driver calls the typing confirmed (it finds the old copy):
// the line gets no Return (found live with "git push" over "git pu").
func TestTerminalRepeatedLineNeedsNewCopy(t *testing.T) {
	r := term(t, "", "$ git push\n$ ", func(s string) string { return s[:min(6, len(s))] })
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.OK}
	res := r.run(protocol.Step{Type: str("git push\n")})
	mustStop(t, res, 0, protocol.ErrNotShown)
	if termSeq(r.d) != "type:git push" {
		t.Fatal(termSeq(r.d))
	}
}
