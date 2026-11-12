package plan

import (
	"testing"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
)

// A live save panel's "Save As:" label is static text whose words are its
// value, not its name; the panel is still a chooser (found live: a typed
// name went in after the selected base name, saving "taken.txt.txt").
func TestChooserLabelInValue(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	saveAs(r, true)
	label := r.d.Node("label")
	v := "Save As:"
	label.Name, label.Role, label.Value = "", "text", &v
	r.d.onDriver = panel(r, nil)
	res := r.run(protocol.Step{Type: str("report.txt\n")})
	mustPass(t, res)
	if !selectAll(r.d) {
		t.Fatalf("the name was not selected first: %s", r.d)
	}
}

// A save panel shown as a sheet is read as part of its document window,
// which is the window in front (found live in the fixture's Save As).
func TestChooserAsSheetInWindow(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	saveAs(r, true)
	r.d.Window("cg:5").Role = "window"
	r.d.Trees["cg:5"][0].Role = "window"
	r.d.Add("cg:5", "sheet", "sheet", "save", nil, "cg:5")
	label := r.d.Node("label")
	v := "Save As:"
	label.Name, label.Role, label.Value = "", "text", &v
	r.d.onDriver = panel(r, nil)
	res := r.run(protocol.Step{Type: str("report.txt\n")})
	mustPass(t, res)
	if !selectAll(r.d) {
		t.Fatalf("the name was not selected first: %s", r.d)
	}
}

// A shortcut that opens the save panel late as a sheet of the front
// window: the line is typed as into a chooser once the sheet shows, though
// the window was read before it came (found live in the fixture's
// File > Save As..., which saved "report-1.txt.txt").
func TestLateSheetPanelAfterShortcut(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	opened := false
	r.d.onDriver = func(tool string, args map[string]any) {
		switch tool {
		case "hotkey":
			if !opened {
				opened = true
				v := "Save As:"
				r.d.Add("cg:1", "sheet", "sheet", "save", nil, "cg:1")
				r.d.Add("cg:1", "label", "text", "", nil, "sheet")
				r.d.Node("label").Value = &v
				r.d.Add("cg:1", "name", "textbox", "", nil, "sheet", "focused")
			}
		}
	}
	res := r.run(protocol.Step{Key: "shift+cmd+s"}, protocol.Step{Type: str("report.txt\n")})
	if !selectAll(r.d) {
		t.Fatalf("not typed as into a chooser: %s %s", jsonOf(res), r.d)
	}
}
