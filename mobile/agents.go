package fennecmobile

import (
	"encoding/json"
	"fennec/internal/cloudops"
	"strings"

	"fennec/internal/setup"
)

// CloudAgentsState says which agents a new session can start on the cloud
// computer. An agent the Mac hasn't moved to the cloud computer has no
// sign-in there, and its session would only fail to start (FEN-73).
type CloudAgentsState struct {
	// Connected: a Mac has connected Fennec to this cloud computer. Launch
	// needs its device key, so nothing starts without one.
	Connected bool `json:"connected"`
	// Known: the cloud computer reported its agents. An older cloud
	// computer can't; then Agents is empty and the app offers every agent.
	Known  bool                        `json:"known"`
	Agents map[string]setup.CloudAgent `json:"agents"`
}

// CloudAgents returns CloudAgentsState as JSON.
func (s *Sessions) CloudAgents() (out0 string, err error) {
	defer func() { err = classify(err) }()
	raw, err := s.read(cloudops.CloudAgentsScript())
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(parseCloudAgents(string(raw)))
	return string(data), err
}

func parseCloudAgents(output string) CloudAgentsState {
	state := CloudAgentsState{Agents: map[string]setup.CloudAgent{}}
	for _, line := range strings.Split(output, "\n") {
		name, value, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch name {
		case "device":
			state.Connected = value != ""
		case "agents":
			var agents map[string]setup.CloudAgent
			if json.Unmarshal([]byte(value), &agents) == nil && agents != nil {
				state.Known, state.Agents = true, agents
			}
		}
	}
	return state
}
