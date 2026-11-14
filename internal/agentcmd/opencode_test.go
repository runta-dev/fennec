package agentcmd

import (
	"strings"
	"testing"
)

func TestOpenCodeV2FlagsFollowCommand(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--auto --standalone"},
		{[]string{"run", "hello"}, "run --auto --standalone hello"},
		{[]string{"--log-level", "error", "run", "hello"}, "--log-level error run --auto --standalone hello"},
		{[]string{"--prompt", "run"}, "--auto --standalone --prompt run"},
		{[]string{"auth", "list"}, "auth list"},
	} {
		if got := strings.Join(OpenCodeV2Arguments(tc.args), " "); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
