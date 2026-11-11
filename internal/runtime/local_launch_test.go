package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeLaunchPreservesIdentityAndContext(t *testing.T) {
	var terminal *LocalTerminal
	launches := 0
	cloudCWD := ""
	backend := &LocalRuntime{Home: t.TempDir(), Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			var values []LocalTerminal
			if terminal != nil {
				values = append(values, *terminal)
			}
			return json.Marshal(map[string]any{"terminals": values})
		}
		if args[0] != "run" {
			t.Fatal(args)
		}
		launches++
		for i, arg := range args {
			if arg == "--cwd" && i+1 < len(args) {
				cloudCWD = args[i+1]
			}
		}
		terminal = &LocalTerminal{Address: "t1", State: "live"}
		return json.Marshal(terminal)
	}}
	request := LocalLaunch{ID: strings.Repeat("a", 32), Agent: "codex", LocalDirectory: "/local/project with spaces", Device: "1234abcd", Arguments: []string{}}
	for attempt := 0; attempt < 2; attempt++ {
		receipt, err := backend.Launch(context.Background(), request)
		if err != nil || receipt.ID != request.ID || receipt.Address != "t1" {
			t.Fatal(receipt, err)
		}
	}
	if launches != 1 {
		t.Fatal("duplicate agent launched", launches)
	}
	if cloudCWD != filepath.Join(backend.Home, ".fen", request.ID) {
		t.Fatal("wrong agent cwd", cloudCWD)
	}
	data, err := os.ReadFile(filepath.Join(cloudCWD, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), request.LocalDirectory) || !strings.Contains(string(data), "fennec-device-"+request.Device) || !strings.Contains(string(data), "uncommitted changes") {
		t.Fatal("wrong local context")
	}
	if _, err := os.Stat(filepath.Join(cloudCWD, ".fennec-local-context.json")); !os.IsNotExist(err) {
		t.Fatal("unexpected separate agent context file", err)
	}
	request.Agent = "pi"
	if _, err := backend.Launch(context.Background(), request); err == nil {
		t.Fatal("identity reused for another agent")
	}
}

func TestNativeLaunchDoesNotReplayUnconfirmedSpawn(t *testing.T) {
	launches := 0
	backend := &LocalRuntime{Home: t.TempDir(), Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		launches++
		return nil, errors.New("lost spawn result")
	}}
	request := LocalLaunch{ID: strings.Repeat("b", 32), Agent: "codex", LocalDirectory: "/project", Device: "1234abcd"}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := backend.Launch(context.Background(), request); err == nil {
			t.Fatal("unknown outcome accepted")
		}
	}
	if launches != 1 {
		t.Fatal("unconfirmed launch replayed", launches)
	}
}
