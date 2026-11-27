package terminalstream

import (
	"context"
	"encoding/json"
	"fennec/internal/cloudops"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreparationHealthReusesResidentProtocolAndReportsLiveFailure(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".local/bin/node", ".local/bin/herdr-core", ".local/bin/codex", ".local/fennec/bin/fennec-terminal"} {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	health := &setup.PreparationHealth{Fingerprint: strings.Repeat("a", 64), Marker: "prepared-runtime", Agents: []string{"codex"}}
	marker := filepath.Join(home, ".config/fennec/prepared-runtime")
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(health.Fingerprint), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, server := net.Pipe()
	done := make(chan error, 1)
	runtime := cloudruntime.NewLocalRuntime(home)
	go func() {
		defer server.Close()
		done <- ServeWithOperations(ctx, server, server, nil, nil, nil, cloudops.Operator(runtime))
	}()
	transport := StartPipes(client, client)
	defer func() { transport.Close(); <-done }()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports("preparation_health") {
		t.Fatal("missing capability")
	}
	op := cloudops.Operation{Kind: "preparation_health", Preparation: health}
	raw, err := transport.Operate(ctx, op)
	var result struct {
		Healthy bool `json:"healthy"`
	}
	if err != nil || json.Unmarshal(raw, &result) != nil || !result.Healthy {
		t.Fatalf("health: %s %v", raw, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Operate(ctx, op); err == nil {
		t.Fatal("removed checkpoint accepted")
	}
	if err := transport.Probe(ctx); err != nil {
		t.Fatal("failed check destroyed shared transport", err)
	}
}
