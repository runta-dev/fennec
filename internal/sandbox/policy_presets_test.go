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

func TestAutoModeRequiresApprovalForCredentialsAndHooks(t *testing.T) {
	policy := PolicyForPreset(PresetAuto, nil)
	for _, kind := range []string{"hook", "tcp_forward", "exec"} {
		if !RequiresPrompt(policy, "/Users/test", "cat ~/.ssh/config", kind) {
			t.Fatalf("credentials should still require approval in auto mode (%s)", kind)
		}
	}
	if RequiresPrompt(policy, "/Users/test", "git status", "sftp") {
		t.Fatal("file transfer in auto mode must remain gated by the sandbox, not a blanket trust bypass")
	}
	if RequiresPrompt(policy, "/Users/test", "git status", "exec") {
		t.Fatal("ordinary workspace reads should not require repeated approval in auto mode")
	}
	if RequiresPrompt(PolicyForPreset(PresetFull, nil), "/Users/test", "cat ~/.ssh/config", "exec") {
		t.Fatal("full mode must not prompt again")
	}
}
