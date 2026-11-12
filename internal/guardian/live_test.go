package guardian

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveResponsesRequest sends one real review request with the local Codex
// sign-in. Run with FENNEC_LIVE_REVIEW=1; it never runs in ordinary tests.
// A usage-limited account is a pass: the backend accepted auth and headers.
func TestLiveResponsesRequest(t *testing.T) {
	if os.Getenv("FENNEC_LIVE_REVIEW") != "1" {
		t.Skip("set FENNEC_LIVE_REVIEW=1 to call the real Responses API")
	}
	home, _ := os.UserHomeDir()
	auth, err := loadCodexAuth(home)
	if err != nil {
		t.Fatal("no local Codex sign-in")
	}
	r := &Reviewer{Home: home}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := time.Now()
	text, status, err := r.post(ctx, auth, defaultModel, Instructions(), RequestMessage(Action{Kind: "exec", Command: "touch /tmp/fennec-live-review", Cwd: home, Justification: "create a scratch file"}, nil))
	t.Logf("status=%d elapsed=%s err=%v text=%q", status, time.Since(start).Round(time.Millisecond), err, text)
	switch {
	case err == nil:
		if _, parseErr := ParseAssessment([]byte(text)); parseErr != nil {
			t.Fatalf("unparseable verdict: %v", parseErr)
		}
	case errors.Is(err, errUnavailable) && status == http.StatusTooManyRequests:
		t.Log("account usage limit reached; request shape and auth were accepted")
	default:
		t.Fatalf("request rejected: %v", err)
	}
}
