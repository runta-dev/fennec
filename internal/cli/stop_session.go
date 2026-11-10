package cli

import (
	"encoding/json"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"os"
)

// Both UI and CLI kill requests use the daemon's authenticated shared connection.
// Never fall back to a second remote mutation when a reply is lost.
func stopSessionShared(input io.Reader, output io.Writer) error {
	defer launchprofile.Begin("session.stop.shared_total")()
	var request struct {
		RuntimeID string `json:"runtime_id"`
		SessionID string `json:"session_id"`
		Address   string `json:"address"`
	}
	if err := json.NewDecoder(io.LimitReader(input, 65536)).Decode(&request); err != nil {
		return err
	}
	if request.RuntimeID == "" || !fenID.MatchString(request.SessionID) {
		return fmt.Errorf("runtime and session identity required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	reply, err := fenLocalRequest(home, fenServiceRequest{Operation: "kill", SessionID: request.SessionID, RuntimeID: request.RuntimeID, Address: request.Address})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"state": "finished", "session_id": request.SessionID, "sessions": reply.Sessions})
}
