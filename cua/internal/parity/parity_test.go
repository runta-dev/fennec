package parity

import (
	"context"
	"testing"

	"fennec/cua/internal/mcpclient"
)

type fakeFixture struct {
	scene string
	state map[string]any
}

func (f *fakeFixture) Reset(_ context.Context, scene string) error {
	f.scene = scene
	f.state = map[string]any{"name": ""}
	return nil
}

func (f *fakeFixture) State(context.Context) (map[string]any, error) { return f.state, nil }

func TestRunComparesOutcomeAndState(t *testing.T) {
	fx := &fakeFixture{}
	setName := func(v string, o Outcome) Driver {
		return func(context.Context, *mcpclient.Client) (Outcome, error) {
			fx.state["name"] = v
			return o, nil
		}
	}
	client := &mcpclient.Client{}
	b := Backends{Fennec: client, Shuttle: client}
	results := Run(context.Background(), fx, b, []Scenario{
		{ID: "same", Capabilities: []string{"CAP-A-001"}, Fennec: setName("Alice", Outcome{OK: true, Calls: 1}), Shuttle: setName("Alice", Outcome{OK: true, Calls: 3})},
		{ID: "state", Capabilities: []string{"CAP-B-001"}, Fennec: setName("Alice", Outcome{OK: true}), Shuttle: setName("AliceAlice", Outcome{OK: true})},
		{ID: "class", Capabilities: []string{"CAP-C-001"}, Fennec: setName("", Outcome{Class: "target_not_found"}), Shuttle: setName("", Outcome{Class: "not_allowed"})},
	})
	want := map[string]bool{"same": true, "state": false, "class": false}
	for _, r := range results {
		if r.Agree != want[r.Scenario] {
			t.Errorf("%s: agree %v (%s)", r.Scenario, r.Agree, r.Why)
		}
	}
	if got := Missing([]string{"CAP-A-001", "CAP-B-001", "CAP-D-001"}, results); len(got) != 2 || got[0] != "CAP-B-001" || got[1] != "CAP-D-001" {
		t.Errorf("missing %v", got)
	}
}
