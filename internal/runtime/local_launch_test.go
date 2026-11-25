package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fennec/internal/clipboardbridge"
	"fennec/internal/gateway"
	"fennec/internal/hookbridge"
	"fmt"
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

func TestOpenCodeLaunchVersionAndSubcommand(t *testing.T) {
	for _, version := range []string{"1.18.18", "opencode v2.3.0"} {
		t.Run(version, func(t *testing.T) {
			home := t.TempDir()
			binary := filepath.Join(home, "opencode")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(home, ".local/fennec"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".local/fennec/agent-mode.json"), []byte(`{"native":false}`), 0600); err != nil {
				t.Fatal(err)
			}
			var command []string
			backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return binary, nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "ls" {
					return []byte(`{"terminals":[]}`), nil
				}
				for i, arg := range args {
					if arg == "--" {
						command = args[i+1:]
						break
					}
				}
				return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
			}}
			_, err := backend.Launch(context.Background(), LocalLaunch{ID: "opencode-probe", Agent: "opencode", LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"run", "hello"}})
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimPrefix(strings.Join(command, " "), "env OPENCODE_DISABLE_AUTOUPDATE=1 env FENNEC_GATEWAY_DEPENDENCY=1 ")
			if strings.Contains(version, "v2") {
				if got != binary+" run --auto --standalone hello" {
					t.Fatal(got)
				}
			} else if got != "env OPENCODE_PERMISSION={\"*\":\"allow\"} "+binary+" run hello" {
				t.Fatal(got)
			}
		})
	}
}

// Pi shows the terminal cursor at the insertion point (PI_HARDWARE_CURSOR=1)
// so the predictive echo can locate its composer, in both launch paths.
func TestPiLaunchShowsHardwareCursor(t *testing.T) {
	var command []string
	backend := &LocalRuntime{Home: t.TempDir(), Lookup: func(string) (string, error) { return "/usr/bin/pi", nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		for i, arg := range args {
			if arg == "--" {
				command = append([]string{}, args[i+1:]...)
			}
		}
		return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
	}}
	request := LocalLaunch{ID: strings.Repeat("e", 32), Agent: "pi", LocalDirectory: "/project", Device: "1234abcd"}
	if _, err := backend.Launch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(command, " "); !strings.HasPrefix(joined, "env PI_SKIP_VERSION_CHECK=1 env PI_HARDWARE_CURSOR=1 ") {
		t.Fatalf("pi command %q", joined)
	}
	script, err := LaunchScriptWithArgs(strings.Repeat("f", 32), "pi", "/tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `if request['agent']=='pi':command=['env','PI_HARDWARE_CURSOR=1']+command`) {
		t.Fatal("cloud launch script does not show Pi's hardware cursor")
	}
}

// Claude Code shows the terminal cursor at the insertion point
// (CLAUDE_CODE_NATIVE_CURSOR=1) in both launch paths and both agent modes, so
// predictive echo does not depend on Claude Code's remote cursor flag.
func TestClaudeLaunchShowsNativeCursor(t *testing.T) {
	for _, native := range []bool{false, true} {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".local/fennec"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".local/fennec/agent-mode.json"), []byte(fmt.Sprintf(`{"native":%v}`, native)), 0600); err != nil {
			t.Fatal(err)
		}
		var command []string
		backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/usr/bin/claude", nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "ls" {
				return []byte(`{"terminals":[]}`), nil
			}
			for i, arg := range args {
				if arg == "--" {
					command = append([]string{}, args[i+1:]...)
				}
			}
			return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
		}}
		request := LocalLaunch{ID: strings.Repeat("c", 32), Agent: "claude", LocalDirectory: "/project", Device: "1234abcd"}
		if _, err := backend.Launch(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, "env CLAUDE_CODE_NATIVE_CURSOR=1 ") || !strings.Contains(joined, "env IS_SANDBOX=1 ") {
			t.Fatalf("native=%v claude command %q", native, joined)
		}
	}
	script, err := LaunchScriptWithArgs(strings.Repeat("f", 32), "claude", "/tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `if request['agent']=='claude':command=['env','CLAUDE_CODE_NATIVE_CURSOR=1']+command`) {
		t.Fatal("cloud launch script does not show Claude Code's native cursor")
	}
}

func TestClaudeLaunchMaterializesSettingsBeforeAgentStarts(t *testing.T) {
	home := t.TempDir()
	backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		found := false
		for i, a := range args {
			if a == "--settings" && i+1 < len(args) {
				found = true
				raw, err := os.ReadFile(args[i+1])
				if err != nil || string(raw) != `{"env":{"PRIVATE":"test-private"}}` {
					t.Fatal("snapshot unavailable at exec", err)
				}
			}
			if strings.Contains(a, "test-private") {
				t.Fatal("private contents in argv")
			}
		}
		if !found {
			t.Fatal("missing settings argument")
		}
		return []byte(`{"address":"t1","state":"live"}`), nil
	}}
	request := LocalLaunch{ID: "calm-fox", Agent: "claude", LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"--settings", hookbridge.SettingsPlaceholder}, ClaudeSettings: []hookbridge.ClaudeSettings{{Index: 1, Data: json.RawMessage(`{"env":{"PRIVATE":"test-private"}}`)}}}
	if _, err := backend.Launch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchAllowsEmptyMacDirectory(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "pi", "grok"} {
		t.Run(agent, func(t *testing.T) {
			if _, err := CloudLaunchScript("cone-leaf", agent, "", "1234abcd", nil); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			cwd, err := prepareSessionWorkspace(home, LocalLaunch{ID: "cone-leaf", Agent: agent, Device: "1234abcd"})
			if err != nil || cwd != filepath.Join(home, ".fen", "cone-leaf") {
				t.Fatal(cwd, err)
			}
		})
	}
	for _, directory := range []string{"relative", "/project\n", "/project\r", "/project\x00"} {
		if (LocalLaunch{ID: "cone-leaf", Agent: "pi", LocalDirectory: directory, Device: "1234abcd"}).Validate() == nil {
			t.Errorf("invalid Mac directory accepted: %q", directory)
		}
	}
}

// agy never goes through Magpie, which would reverse-proxy Google's Gemini
// service: with Magpie on or off it starts on its own (the Mac's) sign-in.
func TestAgyLaunchStaysNativeWithMagpieOn(t *testing.T) {
	for _, native := range []bool{false, true} {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".local/fennec"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".local/fennec/agent-mode.json"), []byte(fmt.Sprintf(`{"native":%v}`, native)), 0600); err != nil {
			t.Fatal(err)
		}
		var command []string
		backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/usr/bin/agy", nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "ls" {
				return []byte(`{"terminals":[]}`), nil
			}
			for i, arg := range args {
				if arg == "--" {
					command = append([]string{}, args[i+1:]...)
				}
			}
			return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
		}}
		request := LocalLaunch{ID: strings.Repeat("a", 32), Agent: "agy", LocalDirectory: "/project", Device: "1234abcd"}
		if _, err := backend.Launch(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, "fennec-terminal native-agent agy --dangerously-skip-permissions") || strings.Contains(joined, "magpie") || strings.Contains(joined, gateway.DependencyEnv) {
			t.Fatalf("native=%v agy command %q", native, joined)
		}
	}
}
