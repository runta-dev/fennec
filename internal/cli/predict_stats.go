package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"fennec/internal/predictstats"
)

const predictStatsUsage = "usage: fen predict-stats [--json] [--all] [--since <24h | 2026-11-16 | 2026-11-16T15:00>]"

// predictStatsCommand prints the local predictive echo statistics, grouped by
// agent and the version in the agent's startup banner. Sessions of recording
// tools are left out unless --all.
func predictStatsCommand(arguments []string, output io.Writer) error {
	asJSON, all := false, false
	var since time.Time
	for i := 0; i < len(arguments); i++ {
		switch arguments[i] {
		case "--json":
			asJSON = true
		case "--all":
			all = true
		case "--since":
			if i+1 == len(arguments) {
				return errors.New(predictStatsUsage)
			}
			i++
			t, err := parseSince(arguments[i], time.Now())
			if err != nil {
				return err
			}
			since = t
		default:
			return errors.New(predictStatsUsage)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	entries, err := predictstats.Load(home)
	if err != nil {
		return err
	}
	summaries := predictstats.Summarize(predictstats.Filter(entries, since, all))
	if asJSON {
		if summaries == nil {
			summaries = []predictstats.Summary{}
		}
		return json.NewEncoder(output).Encode(summaries)
	}
	return predictstats.Write(output, summaries)
}

// parseSince accepts a duration back from now ("24h") or a local date or
// date and time.
func parseSince(value string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q: %s", value, predictStatsUsage)
}
