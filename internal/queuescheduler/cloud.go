package queuescheduler

import (
	"context"
	"encoding/json"
	"fennec/internal/board"
	r "fennec/internal/runtime"
	"fmt"
)

type CloudExecutor struct {
	Runtime *r.LocalRuntime
	Device  string
	Store   board.Store
}

// ListAll asks Herdr for finished terminals too. Its default listing hides
// them, and a run whose terminal finished must be seen as finished, not absent.
var ListAll = map[string]any{"all": true}

func (c CloudExecutor) List(ctx context.Context) ([]Session, error) {
	raw, err := c.Runtime.Query(ctx, "terminal.list", ListAll)
	if err != nil {
		return nil, err
	}
	var list struct {
		Terminals []r.LocalTerminal `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, fmt.Errorf("invalid terminal listing")
	}
	out := []Session{}
	for _, t := range list.Terminals {
		s := NewSession(t.Labels["fennec.session"], t.Address, t.State, t.Title, t.Status, t.Records, t.StatusMS)
		s.Cwd = t.Cwd
		out = append(out, s)
	}
	return out, nil
}
func (c CloudExecutor) Start(ctx context.Context, p board.Project, request board.Request, prompt board.Prompt) error {
	terminal, continued, err := ContinueThread(ctx, c.Runtime, c.Store, p.ID, request.Number)
	if err != nil {
		return err
	}
	return SendRequest(ctx, c.Store, c.Runtime.Query, terminal.Address, request, prompt.For(continued))
}

// Query calls the cloud computer's herdr-core (types answers, THI-176).
func (c CloudExecutor) Query(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.Runtime.Query(ctx, method, params)
}

// End ends a herdr-core terminal; the agent's own session files stay, so a
// reopened thread resumes from them.
func (c CloudExecutor) End(ctx context.Context, address string) error {
	_, err := c.Runtime.Query(ctx, "terminal.kill", map[string]any{"terminal": address})
	return err
}
