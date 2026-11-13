package sandbox

import "testing"

func TestPermissionPresetsKeepCodexSemantics(t *testing.T) {
	for _, tc := range []struct {
		preset, approval, mode string
		network                bool
	}{
		{PresetAsk, PresetAsk, ModeWorkspace, false},
		{PresetAuto, PresetAuto, ModeWorkspace, false},
		{PresetFull, "never", ModeFull, true},
	} {
		policy := PolicyForPreset(tc.preset, []string{"/workspace/project"})
		if policy.Approval != tc.approval || policy.Mode != tc.mode || policy.Network != tc.network {
			t.Fatalf("preset %s produced %+v", tc.preset, policy)
		}
		if len(policy.WritableRoots) != 1 {
			t.Fatalf("writable roots lost: %+v", policy)
		}
	}
	for _, tc := range []struct {
		preset, mode string
	}{
		{"read-only", PresetAsk},
		{"ask-for-approval", PresetAsk},
		{"approve-for-me", PresetAuto},
		{"full-access", PresetFull},
		{"danger-full-access", PresetFull},
		{"workspace", PresetAuto},
		{"full", PresetFull},
	} {
		if got := NormalizePreset(tc.preset); got != tc.mode {
			t.Fatalf("NormalizePreset(%q) = %q, expected %q", tc.preset, got, tc.mode)
		}
	}
}
