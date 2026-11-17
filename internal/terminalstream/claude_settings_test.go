package terminalstream

import (
	"encoding/json"
	"fennec/internal/hookbridge"
	cloudruntime "fennec/internal/runtime"
	"strings"
	"testing"
)

func TestClaudeSettingsRequirePeerCapabilityBeforeDispatch(t *testing.T) {
	launch := &cloudruntime.LocalLaunch{ID: "calm-fox", Agent: "claude", LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"--settings", hookbridge.SettingsPlaceholder}, ClaudeSettings: []hookbridge.ClaudeSettings{{Index: 1, Data: json.RawMessage(`{}`)}}}
	for _, frame := range []Frame{{Type: "launch", ID: "calm-fox", Launch: launch}, {Type: "operation", ID: "calm-fox", Operation: &cloudruntime.LocalOperation{Kind: "launch", Launch: launch}}} {
		// A nil writer intentionally proves the capability check happens before I/O.
		transport := &Transport{}
		if _, err := transport.Launch(frame); err == nil || !strings.Contains(err.Error(), "local Claude settings") {
			t.Fatal("old peer accepted private snapshot", err)
		}
	}
}
