package localssh

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errMacUnreachable = errors.New("Mac reachability failed")

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
	notifyCtx, cancelNotify := context.WithCancel(ctx)
	defer cancelNotify()
	for {
		health.Fresh = first || round%15 == 0
		step, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := config.Health(step, health)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %v", errMacUnreachable, err)
		}
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
		case <-time.After(2 * time.Second):
		}
	}
}
