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
		Terminals []struct {
			State  string            `json:"state"`
			Labels map[string]string `json:"labels"`
		} `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, fmt.Errorf("invalid terminal listing")
	}
	out := []Session{}
	for _, t := range list.Terminals {
		out = append(out, Session{t.Labels["fennec.session"], t.State})
	}
	return out, nil
}
func (c CloudExecutor) Start(ctx context.Context, p board.Project, request board.Request, prompt string) error {
	args := []string{}
	for _, w := range p.Workers {
		if w.Agent == request.Agent && w.Model != "" && (w.Agent == "claude" || w.Agent == "codex") {
			args = append(args, "--model", w.Model)
			break
		}
	}
	args = append(args, prompt)
	device := c.Device
	if device == "" {
		device = c.Store.Routing().Device
	}
	terminal, err := c.Runtime.Launch(ctx, r.LocalLaunch{ID: request.Session, Agent: request.Agent, Device: device, LocalDirectory: "/", Arguments: args})
	if err != nil {
		return err
	}
	_, err = c.Runtime.Query(ctx, "terminal.label", map[string]any{"terminal": terminal.Address, "labels": map[string]string{"fennec.queue": p.ID, "fennec.request": fmt.Sprint(request.Number), "fennec.run": request.Session}})
	return err
}
