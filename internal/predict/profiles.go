package predict

// ProfileFor returns a fresh composer profile for an agent, or nil when the
// agent's composer is not modelled. Only agents that place the real terminal
// cursor at the insertion point can be predicted; agents that draw their own
// cursor (Kimi, Cursor, Pi by default) run without prediction.
func ProfileFor(agent string) Profile {
	switch agent {
	case "claude":
		return newClaudeProfile()
	case "codex":
		return newCodexProfile()
	case "opencode":
		return newOpencodeProfile()
	case "pi":
		return newPiProfile()
	case "kimi":
		return newKimiProfile()
	case "grok":
		return newGrokProfile()
	}
	return nil
}
