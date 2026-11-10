package launchprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func Begin(stage string) func() {
	started := time.Now()
	return func() { Record(stage, started, time.Since(started)) }
}

func Record(stage string, started time.Time, elapsed time.Duration) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	directory := filepath.Join(home, ".config", "fennec")
	if _, err := os.Stat(filepath.Join(directory, "launch-profile.enabled")); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(directory, "launch-profile.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	json.NewEncoder(file).Encode(struct {
		PID     int     `json:"pid"`
		Stage   string  `json:"stage"`
		Started int64   `json:"started_unix_ms"`
		Elapsed float64 `json:"elapsed_ms"`
	}{os.Getpid(), stage, started.UnixMilli(), float64(elapsed.Microseconds()) / 1000})
}
