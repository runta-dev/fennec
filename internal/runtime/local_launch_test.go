package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fennec/internal/clipboardbridge"
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

// Kimi Code runs in Never Ask mode on the cloud computer, through the native
// launcher that points KIMI_CODE_HOME at its synchronized configuration.
func TestKimiLaunchCommand(t *testing.T) {
	var command []string
	var launchArgs []string
	home := t.TempDir()
	backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/usr/bin/kimi", nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		launchArgs = append([]string{}, args...)
		for i, arg := range args {
			if arg == "--" {
				command = append([]string{}, args[i+1:]...)
			}
		}
		return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
	}}
	request := LocalLaunch{ID: strings.Repeat("c", 32), Agent: "kimi", LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"--plan"}}
	terminal, err := backend.Launch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Labels[clipboardbridge.Label] != clipboardbridge.Version || !strings.Contains(strings.Join(launchArgs, " "), "--label "+clipboardbridge.Label+"="+clipboardbridge.Version) {
		t.Fatal("missing persisted clipboard capability", launchArgs, terminal.Labels)
	}
	joined := strings.Join(command, " ")
	bin, _ := clipboardbridge.Bin(home, request.ID)
	if !strings.Contains(joined, clipboardbridge.SessionEnv+"="+request.ID) || !strings.Contains(joined, "XDG_SESSION_TYPE=wayland") || !strings.Contains(joined, "PATH="+bin+":") {
		t.Fatal("missing private clipboard environment", joined)
	}
	for _, name := range []string{"wl-paste", "xclip"} {
		target, err := os.Readlink(filepath.Join(bin, name))
		if err != nil || target != filepath.Join(home, ".local/fennec/bin/fennec-terminal") {
			t.Fatal("missing clipboard adapter", name, err)
		}
	}
	if !strings.Contains(joined, "env KIMI_CODE_TRUST_WORKSPACE=1 ") || !strings.Contains(joined, "native-agent kimi --auto --plan") {
		t.Fatalf("kimi command %q", joined)
	}
	script, err := LaunchScriptWithArgs(strings.Repeat("d", 32), "kimi", "/tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(script, "python3 - '")
	encoded, _, _ := strings.Cut(rest, "'")
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	var payload struct {
		Command []string `json:"command"`
	}
	if json.Unmarshal(raw, &payload) != nil || strings.Join(payload.Command, " ") != "kimi --auto" {
		t.Fatalf("cloud launch command for kimi: %s", raw)
	}
	if !strings.Contains(script, `if request['agent']=='kimi':command=['env','KIMI_CODE_TRUST_WORKSPACE=1']+command`) {
		t.Fatal("cloud launch script does not trust the Kimi session directory")
	}
}
