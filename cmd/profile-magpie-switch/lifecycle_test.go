package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fixtureControl struct {
	state          snapshot
	writes         int
	supersede      bool
	missingCatalog bool
	rejectRestore  bool
}

func (f *fixtureControl) Call(_ context.Context, method string, params, out any) error {
	var value any
	switch method {
	case "daemon.ping":
		value = identity{PID: 1, Build: "fixture"}
	case "connection.status":
		value = connection{Ready: true, Generation: 1}
	case "state.get":
		if f.supersede && f.writes == 1 {
			f.state.Settings.Revision++
			f.supersede = false
		}
		value = f.state
	case "settings.set":
		request := params.(map[string]any)
		if request["expected_revision"].(uint64) != f.state.Settings.Revision {
			return fmt.Errorf("stale revision")
		}
		if f.rejectRestore && f.writes == 1 {
			return fmt.Errorf("restore rejected")
		}
		f.writes++
		mode := request["magpie"].(bool)
		old := f.state
		f.state = readyFixture(mode)
		f.state.Account = old.Account
		f.state.Runtime.Generation = old.Runtime.Generation + 1
		f.state.Settings = preferences{Magpie: mode, Revision: old.Settings.Revision + 1}
		if mode && f.missingCatalog {
			delete(f.state.Runtime.Events, "model_catalog")
		}
		value = f.state.Settings
	default:
		return fmt.Errorf("unexpected RPC %s", method)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func TestSwitchAndRestorationLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		budget                                   time.Duration
		supersede, missingCatalog, rejectRestore bool
		writes                                   int
		failure                                  string
	}{
		{name: "success", budget: time.Second, writes: 2},
		{name: "over budget still restores", budget: time.Nanosecond, writes: 2, failure: "budget"},
		{name: "user superseded", budget: time.Second, supersede: true, writes: 1, failure: "superseded"},
		{name: "unusable result still restores", budget: time.Second, missingCatalog: true, writes: 2, failure: "catalog"},
		{name: "restoration failed", budget: time.Second, rejectRestore: true, writes: 1, failure: "restoration unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := readyFixture(false)
			initial.Account.Epoch = 1
			initial.Account.Status = "authorized"
			f := &fixtureControl{state: initial, supersede: tc.supersede, missingCatalog: tc.missingCatalog, rejectRestore: tc.rejectRestore}
			p := probe{client: f, initial: initial, daemon: identity{PID: 1, Build: "fixture"}, channel: connection{Ready: true, Generation: 1}}
			var output bytes.Buffer
			err := p.measure(json.NewEncoder(&output), tc.budget)
			if tc.failure == "" && err != nil {
				t.Fatal(err)
			}
			if tc.failure != "" && (err == nil || !strings.Contains(err.Error(), tc.failure)) {
				t.Fatalf("expected %s, got %v", tc.failure, err)
			}
			if f.writes != tc.writes {
				t.Fatal("unexpected mutation count", f.writes)
			}
			if f.writes == 2 && f.state.Settings.Magpie != initial.Settings.Magpie {
				t.Fatal("original setting not restored")
			}
		})
	}
}
