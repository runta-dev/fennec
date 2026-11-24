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

// launchNotStarted reports whether the daemon refused a launch before it
// reached the broker: nothing was started, so the same request ID can be sent
// again. Any other failure leaves the outcome unknown.
func launchNotStarted(err error) bool {
	var fault *control.Fault
	return errors.As(err, &fault) && (fault.Code == "runtime_unavailable" || fault.Code == "not_ready")
}

// launchWhenReady sends a launch, and while the daemon refuses it only because
// the cloud computer is not ready yet, sends the same request again until the
// deadline. The request ID makes a repeated launch idempotent.
func launchWhenReady(launch func() error, sessionID any, deadline time.Time, sleep func(time.Duration)) error {
	delay := 250 * time.Millisecond
	for {
		err := launch()
		if err == nil {
			return nil
		}
		if !launchNotStarted(err) {
			return fmt.Errorf("launch %s not confirmed; inspect this request before retrying: %w", sessionID, err)
		}
		if !time.Now().Add(delay).Before(deadline) {
			return fmt.Errorf("launch %s was not started because the cloud computer is not ready yet; try again shortly: %w", sessionID, err)
		}
		sleep(delay)
		delay = min(delay*2, 2*time.Second)
	}
}
