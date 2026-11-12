package localssh

import (
	"context"
	"fennec/internal/sandbox"
	"io"
	"testing"
)

func TestAutoPresetRequiresApprovalAfterRememberedTrustIsCleared(t *testing.T) {
	directory := t.TempDir()
	b := &Broker{pending: map[string]chan Decision{}, output: io.Discard, directory: directory}
	b.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(sandbox.PresetAuto, nil) }
	b.remember("SHA256:device")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b.approve(ctx, "git status", "SHA256:device") {
		t.Fatal("remembered device unexpectedly bypassed approval")
	}
	if err := b.clearTrust(); err != nil {
		t.Fatal(err)
	}
	if !b.approve(context.Background(), "git status", "SHA256:device") {
		t.Fatal("ordinary workspace exec should use sandbox enforcement without another trust prompt")
	}
}

func TestFullPresetSkipsTrustPrompt(t *testing.T) {
	b := &Broker{pending: map[string]chan Decision{}, output: io.Discard}
	b.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(sandbox.PresetFull, nil) }
	if !b.approve(context.Background(), "cat ~/.ssh/config", "SHA256:device") {
		t.Fatal("full preset should bypass repeated trust prompts")
	}
}
