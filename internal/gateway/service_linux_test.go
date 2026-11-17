//go:build linux

package gateway

import (
	"context"
	"errors"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func serviceFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	previous, probe, deps := testUnitDir, probeGateway, gatewayDependencies
	testUnitDir = dir
	gatewayDependencies = func(string) bool { return false }
	t.Cleanup(func() { testUnitDir = previous; probeGateway = probe; gatewayDependencies = deps })
	for name, body := range units(home, "/test/terminal", false) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GATEWAY_TEST_LOG\"\ncase \"$1\" in show) echo success;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("GATEWAY_TEST_LOG", log)
	if err := SetEnabledLocked(home, true); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(log, nil, 0600)
	return home, log
}
func TestRecoveryThresholdBudgetAndCoalescing(t *testing.T) {
	home, log := serviceFixture(t)
	unhealthy := true
	probeGateway = func(context.Context) error {
		if unhealthy {
			return fmt.Errorf("local unresponsive")
		}
		return nil
	}
	// A failed probe followed by healthy status must reset the consecutive count.
	if err := Check(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	unhealthy = false
	if err := Check(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if ReadState(home).Failures != 0 {
		t.Fatal("failure count not reset")
	}
	unhealthy = true
	// Seed the third failure; all concurrent checks must share one recovery.
	writeState(home, State{Status: "recovering", Failures: 2})
	n := 0
	probeGateway = func(context.Context) error {
		n++
		if n == 1 {
			return fmt.Errorf("hung")
		}
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Check(context.Background(), home); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "restart "+Service) != 1 {
		t.Fatal(string(calls))
	}
	if len(ReadState(home).Restarts) != 1 {
		t.Fatal(ReadState(home))
	}
	writeState(home, State{Status: "recovering", Failures: 2, Restarts: []time.Time{time.Now(), time.Now(), time.Now()}})
	probeGateway = func(context.Context) error { return fmt.Errorf("hung") }
	if err := Check(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if ReadState(home).Status != "failed" {
		t.Fatal(ReadState(home))
	}
	before, _ := os.ReadFile(log)
	Check(context.Background(), home)
	after, _ := os.ReadFile(log)
	if string(before) != string(after) {
		t.Fatal("circuit breaker continued restarting")
	}
}
func TestOffDrainsDependenciesWithoutProbingWhenUnused(t *testing.T) {
	home, log := serviceFixture(t)
	if err := SetEnabledLocked(home, false); err != nil {
		t.Fatal(err)
	}
	gatewayDependencies = func(string) bool { return true }
	calls := 0
	probeGateway = func(context.Context) error { calls++; return nil }
	if err := Check(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("dependent session lost gateway")
	}
	gatewayDependencies = func(string) bool { return false }
	if err := Check(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || ReadState(home).Status != "disabled" {
		t.Fatal(ReadState(home), calls)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "disable --now "+Service) {
		t.Fatal(string(b))
	}
	if err := SetEnabledLocked(home, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "enable --now "+Service) {
		t.Fatal("enable failed to restore timer")
	}
}
func TestUnitsAndLockCancellation(t *testing.T) {
	home, _ := serviceFixture(t)
	service := units(home, "/tmp/helper", false)[Service]
	for _, field := range []string{"Restart=always", "StartLimitBurst=5", "KillMode=mixed", "CPUWeight=100", "MAGPIE_ADDR=127.0.0.1:3425"} {
		if !strings.Contains(service, field) {
			t.Fatal(field)
		}
	}
	if installed(t.TempDir()) {
		t.Fatal("adopted another user's unit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := WithLock(context.Background(), home, func() error {
		if err := WithLock(ctx, home, func() error { t.Fatal("entered locked mutation"); return nil }); err == nil {
			t.Fatal("lock did not cancel")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessTimeoutRemainsRetryable(t *testing.T) {
	serviceFixture(t)
	probeGateway = func(context.Context) error { return fmt.Errorf("unavailable") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHealthy(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost retryable cancellation: %v", err)
	}
}
