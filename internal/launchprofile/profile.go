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

// Session records an identity only in the opt-in private profiler log. It lets
// the harness clean up its exact test process after detach without touching peers.
func Session(stage, id string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	directory := filepath.Join(home, ".config/fennec")
	if _, err := os.Stat(filepath.Join(directory, "launch-profile.enabled")); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(directory, "launch-profile.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	json.NewEncoder(file).Encode(map[string]any{"pid": os.Getpid(), "stage": stage, "session_id": id, "started_unix_ms": time.Now().UnixMilli(), "elapsed_ms": 0})
}
