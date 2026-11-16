package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"fennec/internal/predictstats"
)

// predictStatsCommand prints the local predictive echo statistics, grouped by
// agent and the version the agent showed at startup.
func predictStatsCommand(arguments []string, output io.Writer) error {
	asJSON := false
	for _, a := range arguments {
		if a != "--json" {
			return errors.New("usage: fen predict-stats [--json]")
		}
		asJSON = true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	entries, err := predictstats.Load(home)
	if err != nil {
		return err
	}
	summaries := predictstats.Summarize(entries)
	if asJSON {
		if summaries == nil {
			summaries = []predictstats.Summary{}
		}
		return json.NewEncoder(output).Encode(summaries)
	}
	return predictstats.Write(output, summaries)
}
