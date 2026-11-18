package localssh

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errMacUnreachable = errors.New("Mac reachability failed")

// A verified link is torn down, and the relay repaired, only after this many
// consecutive failed probes. The probe is a real ssh command through the relay,
// so it shares the link with user transfers: one slow probe during a large
// scp/rsync is congestion, not an outage, and restarting the relay for it
// dropped every in-flight session. Before the first verified probe a single
// failure still triggers repair at once.
const linkUnhealthyAfter = 3

// linkProbeInterval separates probes. A variable for tests.
var linkProbeInterval = 2 * time.Second

// linkProbeTimeout bounds one probe: ProbeMac's multiplexed attempt, its fresh
// fallback, stopping the old master and the final multiplexed attempt.
const linkProbeTimeout = macProbeTimeout + macFreshProbeTimeout + time.Second + macProbeTimeout + time.Second

// monitorLink runs independently of transport ping/pong. The first full probe
// publishes reachability; broadcasting additionally requires a stable interval.
// Failed probes cancel the link's streams so Link reconciles the relay again.
func monitorLink(ctx context.Context, config LinkConfig, device, alias string, emit func(string, string)) error {
	health := LinkHealth{Device: device}
	first := true
	round := 0
	stableSince := time.Time{}
	notifying := false
	eligible := false
	failures := 0
	notifyCtx, cancelNotify := context.WithCancel(ctx)
	defer cancelNotify()
	for {
		health.Fresh = first || round%15 == 0
		step, cancel := context.WithTimeout(ctx, linkProbeTimeout)
		err := config.Health(step, health)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures++
			if first || failures >= linkUnhealthyAfter {
				return fmt.Errorf("%w: %v", errMacUnreachable, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(linkProbeInterval):
			}
			continue
		}
		failures = 0
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if first {
			if config.onlineBatch != nil {
				eligible = config.onlineBatch.recovered(config.RuntimeID, time.Now())
			}
			emit("connected", alias)
			stableSince = time.Now()
			first = false
		}
		if config.onlineBatch != nil {
			config.onlineBatch.LastHealthy = time.Now().UnixNano()
			if err := saveOnlineBatch(config.Directory, *config.onlineBatch); err != nil {
				return err
			}
		}
		if eligible && !notifying && config.Notify != nil && time.Since(stableSince) >= onlineStableWindow {
			previous := onlineBatch{}
			if config.onlineBatch != nil {
				previous = *config.onlineBatch
			}
			batch, err := nextOnlineBatch(previous, config.RuntimeID, time.Now())
			if err != nil {
				return err
			}
			if err = saveOnlineBatch(config.Directory, batch); err != nil {
				return err
			}
			if config.onlineBatch != nil {
				*config.onlineBatch = batch
			}
			health.Generation, health.Since = batch.Generation, batch.Since
			go config.Notify(notifyCtx, health)
			notifying = true
		}
		round++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(linkProbeInterval):
		}
	}
}
