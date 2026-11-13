package localssh

import (
	"context"
	"fmt"
	"os"
	"time"

	"fennec/internal/guardian"
	"fennec/internal/sandbox"
)

// Codex parity: requests that need approval go to the configured reviewer.
// "Ask for approval" asks the user; "Approve for me" asks Codex auto-review and
// only falls back to the user when local review is unavailable or the denial
// circuit breaker has tripped; "Full access" never asks.

const (
	decisionAutoApproved = "auto_review_approved"
	decisionAutoDenied   = "auto_review_denied"
	decisionAutoTimedOut = "auto_review_timeout"
	decisionForbidden    = "policy_forbidden"
)

type approvalRequest struct {
	Display     string // shown to the user and recorded in audit
	Kind        string // "", "tcp_forward", "sftp", "hook"
	Fingerprint string
	Action      guardian.Action
}

type approvalOutcome struct {
	Allowed  bool
	Decision string
	// Message explains a refusal to the coding agent.
	Message string
	// Rationale records the reviewer's reason in private audit details.
	Rationale string
}

func (b *Broker) authorize(ctx context.Context, request approvalRequest) approvalOutcome {
	policy := b.effectivePolicy()
	// Full access is approval policy "never" (Codex). An unset policy keeps
	// asking, so a broker without a configured preset never runs unreviewed.
	if policy.Approval == "never" {
		return approvalOutcome{Allowed: true, Decision: "auto_sandbox"}
	}
	if policy.Approval == sandbox.PresetAuto && b.Reviewer != nil && !b.breakerTripped(request.Fingerprint) {
		started := time.Now()
		result := b.Reviewer.Review(ctx, request.Action)
		// Private diagnostics only (daemon log), never product UI.
		fmt.Fprintf(os.Stderr, "fennec auto-review: verdict=%d in %s\n", result.Verdict, time.Since(started).Round(time.Millisecond))
		switch result.Verdict {
		case guardian.Approved:
			b.recordReview(request.Fingerprint, false)
			return approvalOutcome{Allowed: true, Decision: decisionAutoApproved, Rationale: result.Assessment.Rationale}
		case guardian.Denied:
			// Only completed denials count toward the breaker; review failures do not.
			if result.Assessment.Outcome == "deny" {
				b.recordReview(request.Fingerprint, true)
			}
			return approvalOutcome{Decision: decisionAutoDenied, Message: result.Message, Rationale: result.Assessment.Rationale}
		case guardian.TimedOut:
			return approvalOutcome{Decision: decisionAutoTimedOut, Message: result.Message}
		}
		// Unavailable: ask the user.
	}
	allowed, decision := b.approveWithDecision(ctx, request.Display, request.Fingerprint, request.Kind, request.Action.Justification)
	if allowed || decision == "user_denied" {
		b.resetBreaker(request.Fingerprint)
	}
	return approvalOutcome{Allowed: allowed, Decision: decision}
}

// Codex circuit breaker: 3 consecutive auto-review denials, or 10 in the last
// 50 reviews, stop automatic review. Codex interrupts the turn; Fennec has no
// turn to interrupt, so further requests from that device go to the user until
// the user decides one.
type breakerState struct {
	consecutive int
	window      []bool
}

func (b *Broker) breakerTripped(fingerprint string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.breaker[fingerprint]
	if state == nil {
		return false
	}
	denials := 0
	for _, denied := range state.window {
		if denied {
			denials++
		}
	}
	return state.consecutive >= 3 || denials >= 10
}

func (b *Broker) recordReview(fingerprint string, denied bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.breaker == nil {
		b.breaker = map[string]*breakerState{}
	}
	state := b.breaker[fingerprint]
	if state == nil {
		state = &breakerState{}
		b.breaker[fingerprint] = state
	}
	if denied {
		state.consecutive++
	} else {
		state.consecutive = 0
	}
	state.window = append(state.window, denied)
	if len(state.window) > 50 {
		state.window = state.window[len(state.window)-50:]
	}
}

func (b *Broker) resetBreaker(fingerprint string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.breaker, fingerprint)
}
