package setup

// CloudAgent says whether a new session of one agent can start on this cloud
// computer. It can when the agent has its own sign-in here (moved from the
// Mac, or signed in on the cloud computer) or its model goes through Magpie.
// An agent the Mac never moved to the cloud computer has neither, and its
// session fails to start (FEN-73).
type CloudAgent struct {
	Account string `json:"account"` // NativeAccountView.Status
	Magpie  bool   `json:"magpie,omitempty"`
	Ready   bool   `json:"ready"`
}

// CloudAgentIDs are the agents a phone can start, by launcher ID.
var CloudAgentIDs = []string{"claude", "codex", "pi", "kimi", "cursor", "grok", "opencode", "agy"}

// CloudAgents reports CloudAgent for every agent in CloudAgentIDs.
func CloudAgents(home string) map[string]CloudAgent {
	agents := make(map[string]CloudAgent, len(CloudAgentIDs))
	for _, agent := range CloudAgentIDs {
		view, err := NativeCloudAccount(home, agent)
		state := CloudAgent{Account: view.Status}
		if err != nil {
			state.Account = "unavailable"
		}
		_, state.Magpie = readConfiguredMagpieModel(home, agent)
		state.Ready = state.Account == "signed_in" || state.Magpie
		agents[agent] = state
	}
	return agents
}
