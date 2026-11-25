package cli

import (
	"errors"
	"fennec/internal/control"
	"strings"
	"testing"
	"time"
)

func TestLaunchWaitsOnlyForRefusalsBeforeTheBroker(t *testing.T) {
	notReady := &control.Fault{Code: "runtime_unavailable", Message: "Prepare the shared runtime first", Retryable: true}
	calls := 0
	err := launchWhenReady(func() error {
		calls++
		if calls < 3 {
			return notReady
		}
		return nil
	}, "loyal-bacon", time.Now().Add(time.Minute), func(time.Duration) {})
	if err != nil || calls != 3 {
		t.Fatalf("launch did not wait for readiness: calls=%d err=%v", calls, err)
	}

	// An outcome the daemon cannot vouch for is never repeated.
	calls = 0
	err = launchWhenReady(func() error { calls++; return &control.Fault{Code: "launch_failed", Message: "broker lost"} }, "loyal-bacon", time.Now().Add(time.Minute), func(time.Duration) {})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("an uncertain launch was retried or misreported: calls=%d err=%v", calls, err)
	}
	calls = 0
	err = launchWhenReady(func() error { calls++; return errors.New("connection reset") }, "loyal-bacon", time.Now().Add(time.Minute), func(time.Duration) {})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("a transport failure was retried: calls=%d err=%v", calls, err)
	}

	// Past the deadline the refusal is reported as not started, not as unknown.
	err = launchWhenReady(func() error { return notReady }, "loyal-bacon", time.Now(), func(time.Duration) {})
	if err == nil || strings.Contains(err.Error(), "not confirmed") || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("unexpected final error: %v", err)
	}
}

func TestLaunchRefusedByTheBrokerIsReportedOnce(t *testing.T) {
	calls := 0
	err := launchWhenReady(func() error {
		calls++
		return &control.Fault{Code: "launch_refused", Message: "cloud takeover is enabled, but this agent's model or runtime is not ready; refresh preparation in Fennec"}
	}, "pigeon-basket", time.Now().Add(time.Minute), func(time.Duration) { t.Fatal("waited on a refusal that waiting cannot fix") })
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "was not started: cloud takeover is enabled") || strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unexpected result: calls=%d err=%v", calls, err)
	}
}
