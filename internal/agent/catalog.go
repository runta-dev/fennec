package agent

import "os/exec"

type Agent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Cloud     bool   `json:"cloudSupported"`
}

func Discover() []Agent {
	agents := []Agent{{ID: "claude", Name: "Claude Code"}, {ID: "codex", Name: "Codex CLI"}}
	for index := range agents {
		_, err := exec.LookPath(agents[index].ID)
		agents[index].Installed = err == nil
	}
	return agents
}
