package localssh

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRelayLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("guest relay requires Linux /proc")
	}
	home := t.TempDir()
	device := strings.Repeat("a", 32)
	statePath := filepath.Join(home, ".local/fennec/relay-"+device, "state.json")
	type state struct {
		PID      int    `json:"pid"`
		Revision string `json:"revision"`
	}
	var owned []int
	defer func() {
		for _, pid := range owned {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}()
	run := func() (state, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(relaySetupScript(device))
		output, err := cmd.CombinedOutput()
		var got state
		if err == nil {
			err = json.Unmarshal(output, &got)
		}
		if err != nil {
			t.Logf("setup output: %s", output)
		}
		return got, err
	}
	first, err := run()
	if err != nil {
		t.Fatal(err)
	}
	owned = append(owned, first.PID)
	again, err := run()
	if err != nil || again.PID != first.PID {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	json.Unmarshal(raw, &metadata)
	metadata["revision"] = "old"
	raw, _ = json.Marshal(metadata)
	os.WriteFile(statePath, raw, 0600)
	upgraded, err := run()
	if err != nil {
		t.Fatal(err)
	}
	owned = append(owned, upgraded.PID)
	if upgraded.PID == first.PID || upgraded.Revision == "old" {
		t.Fatal("old relay reused")
	}
	unrelated := exec.Command("sleep", "30")
	if err = unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	metadata["pid"] = unrelated.Process.Pid
	raw, _ = json.Marshal(metadata)
	os.WriteFile(statePath, raw, 0600)
	if _, err = run(); err == nil {
		t.Fatal("unrelated PID accepted")
	}
	if err = unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated process terminated", err)
	}
}
