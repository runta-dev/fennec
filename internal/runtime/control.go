package runtime

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

func StopScript(address, sessionID string) (string, error) {
	if !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(address) || !sessionIDPattern.MatchString(sessionID) {
		return "", fmt.Errorf("valid Herdr address and Fennec session ID required")
	}
	return "exec \"$HOME/.local/fennec/bin/fennec-terminal\" stop " + address + " " + sessionID + "\n", nil
}

func (runtime *LocalRuntime) Stop(ctx context.Context, address, id string) (LocalTerminal, error) {
	started := time.Now()
	terminal, err := runtime.Terminal(ctx, address, id)
	if err != nil {
		return LocalTerminal{}, err
	}
	timings := map[string]float64{"stop_lookup": float64(time.Since(started).Microseconds()) / 1000}
	if terminal.State != "finished" {
		started = time.Now()
		if _, err := runtime.Run(ctx, "kill", address); err != nil {
			return LocalTerminal{}, fmt.Errorf("stop not confirmed; inspect session before retrying")
		}
		timings["stop_kill"] = float64(time.Since(started).Microseconds()) / 1000
		started = time.Now()
		terminal, err = runtime.Terminal(ctx, address, id)
		if err != nil || terminal.State != "finished" {
			return LocalTerminal{}, fmt.Errorf("stop not confirmed; inspect session before retrying")
		}
		timings["stop_confirm"] = float64(time.Since(started).Microseconds()) / 1000
	}
	terminal.Timings = timings
	return terminal, nil
}
