package queuescheduler

import (
	"context"

	"fennec/internal/board"
	"fennec/internal/setup"
)

// FEN-145: the composer's agent popover. Only Claude Code and Codex take a
// model and effort at launch (see queuescheduler.ContinueThread), so the
// other agents offer just their default.
func init() { board.AgentModels = composerAgents }

type composerAgent struct {
	Agent  string                    `json:"agent"`
	Model  string                    `json:"model,omitempty"`
	Effort string                    `json:"effort,omitempty"`
	Models []setup.NativeModelChoice `json:"models"`
}

var (
	claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	// Before Codex has written its models cache, offer its usual levels.
	codexEfforts = []string{"low", "medium", "high", "xhigh"}
)

func composerAgents(ctx context.Context, home string) any {
	ready := setup.CloudAgents(home)
	out := []composerAgent{}
	for _, agent := range setup.CloudAgentIDs {
		if !ready[agent].Ready {
			continue
		}
		item := composerAgent{Agent: agent, Models: []setup.NativeModelChoice{{ID: "", Name: "Default"}}}
		if agent == "claude" || agent == "codex" {
			if state, err := setup.NativeModels(ctx, home, agent, false); err == nil {
				item.Model, item.Effort, item.Models = state.Model, state.Effort, state.Models
			}
			if agent == "codex" && item.Model != "" {
				known := false
				for _, m := range item.Models {
					known = known || m.ID == item.Model
				}
				if !known {
					item.Models = append(item.Models, setup.NativeModelChoice{ID: item.Model, Name: item.Model, Efforts: codexEfforts})
				}
			}
			for i := range item.Models {
				m := &item.Models[i]
				if agent == "claude" && len(m.Efforts) == 0 && m.ID != "haiku" {
					m.Efforts = claudeEfforts
				}
			}
			if agent == "codex" && len(item.Models) > 0 && len(item.Models[0].Efforts) == 0 && len(item.Models) == 1 {
				item.Models[0].Efforts = codexEfforts
			}
			// Default runs the configured model: offer its efforts.
			if len(item.Models) > 0 && item.Models[0].ID == "" && len(item.Models[0].Efforts) == 0 {
				for _, m := range item.Models[1:] {
					if m.ID == item.Model || item.Model == "" && len(m.Efforts) > 0 {
						item.Models[0].Efforts = m.Efforts
						break
					}
				}
			}
		}
		out = append(out, item)
	}
	return out
}
