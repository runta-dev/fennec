package cli

import (
	"strings"
	"testing"

	"fennec/internal/board"
)

func TestLaunchArguments(t *testing.T) {
	if args := launchArguments(board.Worker{Agent: "claude", Model: "opus"}, "go"); strings.Join(args, " ") != "--model opus go" {
		t.Fatal(args)
	}
	if args := launchArguments(board.Worker{Agent: "kimi", Model: "K2"}, "go"); len(args) != 1 {
		t.Fatal(args)
	}
}
