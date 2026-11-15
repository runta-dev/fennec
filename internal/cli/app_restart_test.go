package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAppRestartOrderAndFailureRecovery(t *testing.T) {
	failure := errors.New("failed")
	for _, tc := range []struct {
		name                string
		waitErr, restartErr error
		want                []string
	}{
		{"success", nil, nil, []string{"exit", "daemon", "open"}},
		{"UI remains", failure, nil, []string{"exit"}},
		{"daemon failure still reopens", nil, failure, []string{"exit", "daemon", "open"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := completeAppRestart(ctx,
				func(context.Context) error { calls = append(calls, "exit"); return tc.waitErr },
				func(context.Context) error {
					calls = append(calls, "daemon")
					if tc.restartErr != nil {
						cancel()
					}
					return tc.restartErr
				},
				func(ctx context.Context) error { calls = append(calls, "open"); return ctx.Err() })
			if !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("calls: %v", calls)
			}
			if (err != nil) != (tc.waitErr != nil || tc.restartErr != nil) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestAppRestartDoesNotReplaceConcurrentReplacement(t *testing.T) {
	for _, tc := range []struct {
		retiring, actual int
		satisfied        bool
	}{
		{0, 100, true}, {100, 100, false}, {100, 101, true},
		{-1, 100, false}, {100, 0, false},
	} {
		if got := restartAlreadySatisfied(tc.retiring, tc.actual); got != tc.satisfied {
			t.Fatalf("retiring=%d actual=%d satisfied=%v", tc.retiring, tc.actual, got)
		}
	}
	for _, command := range []string{"app-restart", "app-restart-worker"} {
		if PublicInvocation([]string{command, "123"}) {
			t.Fatalf("%s routed to public client", command)
		}
	}
}

func TestApplicationExitWaitIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitApplicationExit(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
}
