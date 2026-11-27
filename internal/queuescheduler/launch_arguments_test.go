package queuescheduler

import (
	"reflect"
	"testing"

	"fennec/internal/board"
)

// FEN-145: the composer's model and effort reach the agent's command line.
func TestLaunchArgumentsUseTheComposerChoice(t *testing.T) {
	p := board.Project{Workers: []board.Worker{{Agent: "claude", Model: "sonnet", On: true}}}
	for _, tc := range []struct {
		request board.Request
		agent   string
		want    []string
	}{
		{board.Request{Agent: "claude", Model: "opus", Effort: "xhigh"}, "claude", []string{"--model", "opus", "--effort", "xhigh"}},
		{board.Request{Agent: "claude", Effort: "low"}, "claude", []string{"--model", "sonnet", "--effort", "low"}},
		{board.Request{Agent: "codex", Model: "gpt-6.2-sol", Effort: "high"}, "codex", []string{"--model", "gpt-6.2-sol", "-c", "model_reasoning_effort=high"}},
		{board.Request{Agent: "codex", Model: "gpt-6.2-sol"}, "claude", []string{"--model", "sonnet"}},
		{board.Request{Agent: "claude", Model: "opus; rm -rf /"}, "claude", []string{"--model", "sonnet"}},
		{board.Request{Agent: "pi", Model: "x"}, "pi", nil},
	} {
		got := LaunchArguments(p, tc.request, tc.agent)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v as %s: got %q, want %q", tc.request, tc.agent, got, tc.want)
		}
	}
}
