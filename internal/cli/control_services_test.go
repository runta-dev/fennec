package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"os"
	"path/filepath"
	"testing"
)

type serviceCredentials struct{}

func (serviceCredentials) Read(context.Context) (string, error) { return "test-token", nil }
func (serviceCredentials) Write(context.Context, string) error  { return nil }
func (serviceCredentials) Delete(context.Context) error         { return nil }
func TestTakeoverConfigurationDoesNotRestartServices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, serviceCredentials{}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	lifecycle, _, epoch, _ := manager.Session()
	home := t.TempDir()
	s := newDaemonServices(home, manager)
	s.ctx = lifecycle
	s.epoch = epoch
	s.config = brokerConfiguration{RuntimeID: "owned"}
	s.selected = map[string]bool{"codex": true}
	s.ready = map[string]bool{"codex": true}
	s.brokerReady = true
	s.brokerRunning = true
	for i := 0; i < 2; i++ {
		if _, err := s.setTakeover(ctx, json.RawMessage(`{"agent":"codex","enabled":false}`)); err != nil {
			t.Fatal(err)
		}
	}
	if s.ctx != lifecycle || lifecycle.Err() != nil || !s.brokerRunning || !s.brokerReady {
		t.Fatal("preference restarted services")
	}
	data, err := os.ReadFile(filepath.Join(home, ".config/fennec/takeover-preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Agents []string `json:"agents"`
	}
	if json.Unmarshal(data, &saved) != nil || len(saved.Agents) != 0 {
		t.Fatal(string(data))
	}
}
