package plan

import (
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Background input refuses a title two shown windows of one app share,
// even when one of them is active, and takes the only shown window of a
// title over a minimized twin (found live: a background press went to the
// front one of the fixture's "Twin" windows).
func TestBackgroundTwinTitlesRefused(t *testing.T) {
	r := behind(t, "background")
	r.d.Win("cg:3", "Mail", "Inbox", 200, [4]int{120, 120, 900, 700}, false)
	r.d.Add("cg:2", "mark2", "button", "Mark", nil, "cg:2")
	r.d.Add("cg:3", "mark3", "button", "Mark", nil, "cg:3")
	res := r.run(protocol.Step{Press: sel(`button "Mark"`), Window: "Inbox"})
	mustStop(t, res, 0, protocol.ErrTargetAmbiguous)
	if len(r.d.Performs) != 0 {
		t.Fatal(r.d.Performs)
	}
	// A minimized twin is no rival.
	r = behind(t, "background")
	r.d.Win("cg:3", "Mail", "Inbox", 200, [4]int{120, 120, 900, 700}, false).Hidden = "minimized"
	r.d.Add("cg:2", "mark2", "button", "Mark", nil, "cg:2")
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Mark"`), Window: "Inbox"}))
}

// A background click into a field the driver cannot confirm is proven by
// the text the next step types and reads back there (found live: the
// fixture's Note took the text, and the plan still failed on the click).
func TestConfirmedTypeProvesClick(t *testing.T) {
	r := sendRig(t)
	r.d.Replies["click"] = []fakedesk.Reply{fakedesk.Unverified("not reported")}
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.OK}
	r.d.Focus = map[int][]*view.RawNode{200: {r.d.Node("to")}}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`textbox "To"`)}, protocol.Step{Type: str("x"), Window: "Inbox"})
	if !res.OK && res.Error != nil && strings.Contains(res.Error.Message, "step 1 was sent but not confirmed") {
		t.Fatal(jsonOf(res))
	}
}
