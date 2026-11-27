package queuescheduler

import (
	"testing"

	"fennec/internal/board"
)

// FEN-145: every program that serves the board (the cloud's fennec-terminal
// included) answers GET /v1/agents.
func TestAgentModelsIsRegistered(t *testing.T) {
	if board.AgentModels == nil {
		t.Fatal("board.AgentModels is not set")
	}
}
