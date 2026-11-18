package localssh

import (
	"context"
	"io"
	"strings"
	"testing"

	"fennec/internal/sandbox"
)

func TestUserCanAllowCommandAfterAutoReviewDenial(t *testing.T) {
	directory := t.TempDir()
	b := brokerFor(sandbox.PresetAuto, reviewer(t, `{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"Risky."}`, false), io.Discard)
	b.directory = directory
	denied := b.authorize(context.Background(), request("git push --force"))
	if denied.Allowed || denied.Decision != decisionAutoDenied || !Grantable(denied.Decision) || !strings.Contains(denied.Message, allowHint) {
		t.Fatalf("denial must tell the agent the user can allow it: %+v", denied)
	}
	if err := AllowGrant(directory, GrantKey("SHA256:device", "git push --force")); err != nil {
		t.Fatal(err)
	}
	allowed := b.authorize(context.Background(), request("git push --force"))
	if !allowed.Allowed || allowed.Decision != decisionUserAllowed {
		t.Fatalf("allowed command must run without review: %+v", allowed)
	}
	if other := b.authorize(context.Background(), request("git push --force origin main")); other.Allowed {
		t.Fatalf("grant must cover only the exact command: %+v", other)
	}
	other := request("git push --force")
	other.Fingerprint = "SHA256:other"
	if got := b.authorize(context.Background(), other); got.Allowed {
		t.Fatalf("grant must cover only the same device: %+v", got)
	}
	if err := b.clearTrust(); err != nil {
		t.Fatal(err)
	}
	if Granted(directory, GrantKey("SHA256:device", "git push --force")) {
		t.Fatal("clearing device trust must clear grants")
	}
}

func TestAllowGrantRejectsInvalidKeys(t *testing.T) {
	if AllowGrant(t.TempDir(), "not-a-hash") == nil || GrantKey("", "ls") != "" {
		t.Fatal("invalid grants must be rejected")
	}
}
