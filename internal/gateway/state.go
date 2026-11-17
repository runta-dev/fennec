// Package gateway manages the cloud-local model gateway independently of SSH.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const Service = "fennec-magpie.service"
const DependencyEnv = "FENNEC_GATEWAY_DEPENDENCY"

type State struct {
	Status   string      `json:"status"`
	Reason   string      `json:"reason,omitempty"`
	Failures int         `json:"failures,omitempty"`
	Restarts []time.Time `json:"restarts,omitempty"`
	Updated  time.Time   `json:"updated"`
}

func statePath(home string) string { return filepath.Join(home, ".config/fennec/gateway-state.json") }
func ReadState(home string) State {
	var s State
	b, err := os.ReadFile(statePath(home))
	if err != nil || json.Unmarshal(b, &s) != nil {
		return State{Status: "unknown"}
	}
	// A stale healthy snapshot is not evidence of current availability.
	if s.Status == "healthy" && time.Since(s.Updated) > 30*time.Second {
		s.Status = "unknown"
	}
	return s
}
func writeState(home string, s State) error {
	s.Updated = time.Now().UTC()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicWrite(statePath(home), b, 0600)
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gateway-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// Magpie has no /health endpoint. Its deliberately unknown local route returns
// this structured 404 without looking up a provider or making an inference.
// Catalog validation remains a separate preparation check.
func Probe(ctx context.Context, client *http.Client, address string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", address+"/_fennec/liveness", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway unreachable")
	}
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if resp.StatusCode != http.StatusNotFound || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil || body.Error.Type != "not_found_error" {
		return fmt.Errorf("unexpected gateway response")
	}
	return nil
}
func localProbe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Never route local health through inherited proxy variables.
	return Probe(ctx, &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}, "http://127.0.0.1:3425")
}
