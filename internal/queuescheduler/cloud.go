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

func (c CloudExecutor) List(ctx context.Context) ([]Session, error) {
	raw, err := c.Runtime.Query(ctx, "terminal.list", map[string]any{})
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
		out = append(out, Session{ID: t.Labels["fennec.session"], State: t.State, Title: t.Title})
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
