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
		out = append(out, Session{ID: t.Labels["fennec.session"], Address: t.Address, State: t.State, Title: t.Title, Status: AgentStatus(t.Status), StatusAt: StatusTime(t.StatusMS)})
	}
	return out, nil
}
func (c CloudExecutor) Start(ctx context.Context, p board.Project, request board.Request, prompt string) error {
	terminal, err := c.Runtime.ThreadSession(ctx, c.Store, p.ID, request.Number)
	if err != nil {
		return err
	}
	return SendRequest(ctx, c.Store, c.Runtime.Query, terminal.Address, request, prompt)
}

// End ends a herdr-core terminal; the agent's own session files stay, so a
// reopened thread resumes from them.
func (c CloudExecutor) End(ctx context.Context, address string) error {
	_, err := c.Runtime.Query(ctx, "terminal.kill", map[string]any{"terminal": address})
	return err
}
