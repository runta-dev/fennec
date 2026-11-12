package cli

import (
	"fennec/internal/sandbox"
	"testing"
)

func TestEffectivePermissionPresetCompatibility(t *testing.T) {
	for _, tc := range []struct {
		permission, sandboxMode, want string
	}{
		{"", "", sandbox.PresetAuto},
		{"", "full", sandbox.PresetFull},
		{"", "workspace", sandbox.PresetAuto},
		{"", "read-only", sandbox.PresetAsk},
		{sandbox.PresetAsk, "full", sandbox.PresetAsk},
		{"full-access", "", sandbox.PresetFull},
		{"ask-for-approval", "", sandbox.PresetAsk},
		{"approve-for-me", "", sandbox.PresetAuto},
	} {
		prefs := desktopPreferences{PermissionMode: tc.permission, SandboxMode: tc.sandboxMode}
		if got := effectivePermissionPreset(prefs); got != tc.want {
			t.Fatalf("effectivePermissionPreset(%+v) = %q, expected %q", prefs, got, tc.want)
		}
	}
}
