// Package parity runs one scenario against fennec-cua and against shuttle and
// compares the outcomes: success or failure class, and the final state the
// fixture app reports. Fields of the two protocols are never compared, since
// they differ by design.
package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"fennec/cua/internal/mcpclient"
)

// Backend names.
const (
	Fennec  = "fennec-cua"
	Shuttle = "shuttle"
)

// Outcome is what a backend's driver function concluded.
type Outcome struct {
	OK bool `json:"ok"`
	// Class is a protocol-neutral failure class such as target_not_found,
	// not_allowed, focus_mismatch; empty on success.
	Class  string `json:"class,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Calls counts tool calls, for the efficiency comparison.
	Calls int `json:"calls"`
}

// Fixture is the app scenarios run against. It resets to a named scene and
// reports its true state, independent of what either backend says.
type Fixture interface {
	Reset(ctx context.Context, scene string) error
	State(ctx context.Context) (map[string]any, error)
}

// Driver performs a scenario's task through one backend.
type Driver func(ctx context.Context, c *mcpclient.Client) (Outcome, error)

// Scenario is one task with a driver per backend.
type Scenario struct {
	ID           string
	Capabilities []string // CAP ids this scenario proves
	Scene        string   // fixture scene to reset to
	Fennec       Driver
	Shuttle      Driver
	Timeout      time.Duration
}

// Side is one backend's run of a scenario.
type Side struct {
	Outcome Outcome        `json:"outcome"`
	State   map[string]any `json:"state"`
	Ms      int64          `json:"ms"`
	Err     string         `json:"error,omitempty"`
}

// Comparison is a scenario's verdict.
type Comparison struct {
	Scenario     string   `json:"scenario"`
	Capabilities []string `json:"capabilities"`
	Fennec       Side     `json:"fennec"`
	Shuttle      Side     `json:"shuttle"`
	Agree        bool     `json:"agree"`
	Why          string   `json:"why,omitempty"`
}

// Backends holds a session per backend.
type Backends struct {
	Fennec  *mcpclient.Client
	Shuttle *mcpclient.Client
}

// Run runs each scenario on both backends, resetting the fixture before
// each side.
func Run(ctx context.Context, fx Fixture, b Backends, scenarios []Scenario) []Comparison {
	out := make([]Comparison, 0, len(scenarios))
	for _, s := range scenarios {
		c := Comparison{Scenario: s.ID, Capabilities: s.Capabilities}
		c.Fennec = side(ctx, fx, s, s.Fennec, b.Fennec)
		c.Shuttle = side(ctx, fx, s, s.Shuttle, b.Shuttle)
		c.Agree, c.Why = agree(c.Fennec, c.Shuttle)
		out = append(out, c)
	}
	return out
}

func side(ctx context.Context, fx Fixture, s Scenario, d Driver, c *mcpclient.Client) Side {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if d == nil || c == nil {
		return Side{Err: "no driver or backend"}
	}
	if err := fx.Reset(ctx, s.Scene); err != nil {
		return Side{Err: "reset: " + err.Error()}
	}
	start := time.Now()
	o, err := d(ctx, c)
	sd := Side{Outcome: o, Ms: time.Since(start).Milliseconds()}
	if err != nil {
		sd.Err = err.Error()
	}
	st, err := fx.State(ctx)
	if err != nil && sd.Err == "" {
		sd.Err = "state: " + err.Error()
	}
	sd.State = st
	return sd
}

func agree(f, s Side) (bool, string) {
	switch {
	case f.Err != "" || s.Err != "":
		return false, fmt.Sprintf("harness error: fennec %q, shuttle %q", f.Err, s.Err)
	case f.Outcome.OK != s.Outcome.OK:
		return false, fmt.Sprintf("ok differs: fennec %v, shuttle %v", f.Outcome.OK, s.Outcome.OK)
	case f.Outcome.Class != s.Outcome.Class:
		return false, fmt.Sprintf("failure class differs: fennec %q, shuttle %q", f.Outcome.Class, s.Outcome.Class)
	case !reflect.DeepEqual(normal(f.State), normal(s.State)):
		return false, "final fixture state differs"
	}
	return true, ""
}

// normal round-trips through JSON so numbers and nested maps compare alike.
func normal(m map[string]any) any {
	data, _ := json.Marshal(m)
	var v any
	_ = json.Unmarshal(data, &v)
	return v
}

// Missing returns the oracle capabilities no agreeing scenario proves.
func Missing(oracle []string, results []Comparison) []string {
	proven := map[string]bool{}
	for _, r := range results {
		if !r.Agree {
			continue
		}
		for _, c := range r.Capabilities {
			proven[c] = true
		}
	}
	var missing []string
	for _, c := range oracle {
		if !proven[c] {
			missing = append(missing, c)
		}
	}
	return missing
}
