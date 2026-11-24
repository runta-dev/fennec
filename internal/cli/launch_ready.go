package cli

import (
	"errors"
	"fennec/internal/control"
	"fmt"
	"time"
)

// launchReadyWait bounds how long `fen launch` waits for the cloud computer's
// services when the daemon is still bringing them up.
const launchReadyWait = 90 * time.Second

// launchFault returns the daemon's fault code for a failed launch, or "" when
// the failure did not come from the daemon (for example a lost connection).
func launchFault(err error) string {
	var fault *control.Fault
	if errors.As(err, &fault) {
		return fault.Code
	}
	return ""
}

// launchWhenReady sends a launch and classifies its failure:
//
//   - runtime_unavailable, not_ready: refused before the broker because the
//     cloud computer is still coming up. Nothing started, so the same request
//     ID is sent again until the deadline.
//   - launch_refused: the broker refused before anything started (the agent
//     is not enabled, not signed in, or the directory is not authorized).
//     Waiting does not help; the reason is reported as is.
//   - anything else: the outcome is unknown and is never repeated.
func launchWhenReady(launch func() error, sessionID any, deadline time.Time, sleep func(time.Duration)) error {
	delay := 250 * time.Millisecond
	for {
		err := launch()
		if err == nil {
			return nil
		}
		switch launchFault(err) {
		case "runtime_unavailable", "not_ready":
			if !time.Now().Add(delay).Before(deadline) {
				return fmt.Errorf("launch %s was not started because the cloud computer is not ready yet; try again shortly: %w", sessionID, err)
			}
			sleep(delay)
			delay = min(delay*2, 2*time.Second)
		case "launch_refused":
			return fmt.Errorf("launch %s was not started: %w", sessionID, err)
		default:
			return fmt.Errorf("launch %s not confirmed; inspect this request before retrying: %w", sessionID, err)
		}
	}
}
