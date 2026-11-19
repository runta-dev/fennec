package cli

import (
	"strings"
	"testing"

	"fennec/internal/board"
)

func TestBoardLaunchMessage(t *testing.T) {
	p := board.Project{ID: "fennec", Key: "FEN", Name: "Fennec", Repository: "runta-dev/fennec", Tags: board.DefaultTags}
	request := board.Request{Number: 42, Title: "Queues overflow", Tag: "bug", Status: board.Working,
		Messages: []board.Message{{ID: 1, Author: "shiqi", Body: "On iPhone it scrolls sideways."}}}
	message := firstMessage(p, request, "claude")
	for _, want := range []string{"FEN-42: Queues overflow", "Tag: Bug", "On iPhone it scrolls sideways.", "~/repos/runta-dev/fennec", "fennec-terminal reply FEN-42", "projects/fennec/knowledge/", "by: claude"} {
		if !strings.Contains(message, want) {
			t.Errorf("first message lacks %q:\n%s", want, message)
		}
	}
	if args := launchArguments(board.Worker{Agent: "claude", Model: "opus"}, "go"); strings.Join(args, " ") != "--model opus go" {
		t.Fatal(args)
	}
	if args := launchArguments(board.Worker{Agent: "kimi", Model: "K2"}, "go"); len(args) != 1 {
		t.Fatal(args)
	}
}
