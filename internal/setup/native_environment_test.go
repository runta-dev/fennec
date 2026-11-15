package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativeEnvironmentCoalescesPrewarmAndHonorsCanceledWaiter(t *testing.T) {
	directory := t.TempDir()
	shell := filepath.Join(directory, "shell")
	calls, gate := filepath.Join(directory, "calls"), filepath.Join(directory, "gate")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'call\\n' >> %q\nwhile [ ! -f %q ]; do sleep 0.01; done\nprintf '\\0OPENAI_API_KEY=fixture-key\\0'\n", calls, gate)
	if err := os.WriteFile(shell, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); PrewarmNativeEnvironment(ctx) }()
	// Always release the owned fixture, including on a failed assertion.
	defer func() { os.WriteFile(gate, nil, 0600); workers.Wait() }()
	for {
		if _, err := os.Stat(calls); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("shell did not start")
		}
		time.Sleep(time.Millisecond)
	}
	waiter, stop := context.WithCancel(ctx)
	stop()
	done := make(chan struct{})
	go func() { nativeSourceEnvironment(waiter); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter blocked on prewarm")
	}
	results := make(chan map[string]string, 8)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- nativeSourceEnvironment(ctx) }()
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	for i := 0; i < 8; i++ {
		if (<-results)["OPENAI_API_KEY"] != "fixture-key" {
			t.Fatal("discovery result lost")
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(data), "call\n") != 1 {
		t.Fatal("duplicated shell initialization", err, string(data))
	}
}
