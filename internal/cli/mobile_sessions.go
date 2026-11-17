package cli

// SessionSummary is the session row the Mac panel shows, exported for the
// iPhone app so both clients parse cloud listings with the same rules.
type SessionSummary struct {
	RuntimeID string `json:"runtime_id"`
	Address   string `json:"address"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Title     string `json:"title"`
	Status    string `json:"status"`
}

// ParseSessions applies the Mac panel's filtering, agent detection, title
// cleanup and status labels to a `fennec-terminal list` result.
func ParseSessions(data []byte, runtimeID string) ([]SessionSummary, error) {
	sessions, err := fenParseSessions(data, runtimeID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, SessionSummary{
			RuntimeID: session.Receipt.RuntimeID,
			Address:   session.Receipt.Address,
			SessionID: session.Receipt.SessionID,
			Agent:     session.Receipt.Agent,
			Title:     session.Title,
			Status:    session.Status,
		})
	}
	return out, nil
}
