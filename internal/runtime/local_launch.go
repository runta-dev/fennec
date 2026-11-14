package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/hookbridge"
	"fennec/internal/setup"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type LocalLaunch struct {
	NativeLogin    bool                 `json:"native_login,omitempty"`
	Hooks          []hookbridge.Binding `json:"hooks,omitempty"`
	ID             string               `json:"id"`
	Agent          string               `json:"agent"`
	LocalDirectory string               `json:"local_directory"`
	Device         string               `json:"device"`
	Arguments      []string             `json:"arguments,omitempty"`
}

type LocalTerminal struct {
	Title    string             `json:"title,omitempty"`
	Name     string             `json:"name,omitempty"`
	Parent   *string            `json:"parent,omitempty"`
	Status   json.RawMessage    `json:"status,omitempty"`
	Timings  map[string]float64 `json:"timings_ms,omitempty"`
	ID       string             `json:"id,omitempty"`
	Agent    string             `json:"agent,omitempty"`
	Address  string             `json:"address"`
	State    string             `json:"state"`
	ExitCode *int               `json:"exit_code,omitempty"`
	Labels   map[string]string  `json:"labels,omitempty"`
}

type LocalRuntime struct {
	Home   string
	Run    func(context.Context, ...string) ([]byte, error)
	Lookup func(string) (string, error)
}

func (request LocalLaunch) Validate() error {
	if request.NativeLogin && (request.Agent == "opencode" || len(request.Arguments) != 0 || len(request.Hooks) != 0) {
		return fmt.Errorf("invalid native login launch")
	}
	if err := hookbridge.ValidateBindings(request.Hooks); err != nil {
		return err
	}
	if !sessionIDPattern.MatchString(request.ID) || !regexp.MustCompile(`^[a-f0-9]{8,64}$`).MatchString(request.Device) || !filepath.IsAbs(request.LocalDirectory) || strings.ContainsAny(request.LocalDirectory, "\x00\r\n") {
		return fmt.Errorf("invalid local session identity")
	}
	if !map[string]bool{"claude": true, "codex": true, "pi": true, "opencode": true, "kimi": true}[request.Agent] {
		return fmt.Errorf("unsupported agent")
	}
	size := 0
	for _, argument := range request.Arguments {
		size += len(argument)
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("invalid argument")
		}
	}
	if len(request.Arguments) > 256 || size > 32768 {
		return fmt.Errorf("oversized arguments")
	}
	return nil
}

func NewLocalRuntime(home string) *LocalRuntime {
	return &LocalRuntime{Home: home, Lookup: exec.LookPath, Run: func(ctx context.Context, arguments ...string) ([]byte, error) {
		step, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		command := exec.CommandContext(step, filepath.Join(home, ".local/bin/herdr-core"), arguments...)
		return command.Output()
	}}
}

func (runtime *LocalRuntime) Launch(ctx context.Context, request LocalLaunch) (LocalTerminal, error) {
	phase := time.Now()
	timings := map[string]float64{}
	mark := func(stage string) {
		timings[stage] = float64(time.Since(phase).Microseconds()) / 1000
		phase = time.Now()
	}
	if err := ctx.Err(); err != nil {
		return LocalTerminal{}, err
	}
	if err := request.Validate(); err != nil {
		return LocalTerminal{}, err
	}
	root := filepath.Join(runtime.Home, ".local/fennec/launches")
	if err := os.MkdirAll(root, 0700); err != nil {
		return LocalTerminal{}, err
	}
	lock, err := os.OpenFile(filepath.Join(root, request.ID+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return LocalTerminal{}, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return LocalTerminal{}, err
		}
		select {
		case <-ctx.Done():
			return LocalTerminal{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	mark("journal_lock")
	marker := filepath.Join(root, request.ID+".json")
	previous, readErr := os.ReadFile(marker)
	if readErr == nil {
		var stored LocalLaunch
		decodeErr := json.Unmarshal(previous, &stored)
		actual, _ := json.Marshal(stored)
		expected, _ := json.Marshal(request)
		if decodeErr != nil || !bytes.Equal(actual, expected) {
			return LocalTerminal{}, fmt.Errorf("session identity belongs to another launch")
		}
	} else if !os.IsNotExist(readErr) {
		return LocalTerminal{}, readErr
	}
	raw, err := runtime.Run(ctx, "ls", "--all", "--json", "--label", "fennec.session="+request.ID)
	if err != nil {
		return LocalTerminal{}, err
	}
	var existing struct {
		Terminals []LocalTerminal `json:"terminals"`
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &existing) != nil {
		return LocalTerminal{}, fmt.Errorf("invalid session listing")
	}
	if len(existing.Terminals) > 1 {
		return LocalTerminal{}, fmt.Errorf("duplicate session identity")
	}
	mark("session_lookup")
	if len(existing.Terminals) == 1 {
		if strings.Contains(request.ID, "-") && os.IsNotExist(readErr) {
			return LocalTerminal{}, fmt.Errorf("session name already belongs to another launch")
		}
		result := existing.Terminals[0]
		result.ID = request.ID
		result.Agent = request.Agent
		return result, nil
	}
	if readErr == nil {
		return LocalTerminal{}, fmt.Errorf("launch outcome unknown; inspect before retrying")
	}
	binary, err := runtime.Lookup(request.Agent)
	if err != nil {
		// If background agent warmup is still finishing after fast 5s onboarding,
		// wait on the per-agent installation flock / install on demand seamlessly.
		if ensureErr := ensureLocalAgentBinary(ctx, runtime.Home, request.Agent); ensureErr == nil {
			binary, err = runtime.Lookup(request.Agent)
		}
	}
	if err != nil {
		return LocalTerminal{}, fmt.Errorf("cloud agent executable missing")
	}
	cwd, err := prepareSessionWorkspace(runtime.Home, request)
	if err != nil {
		return LocalTerminal{}, err
	}
	command := []string{binary}
	switch request.Agent {
	case "codex":
		command = append(command, "--no-daemon", "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`)
	case "claude":
		command = append(command, "--permission-mode", "bypassPermissions")
		command = append([]string{"env", "IS_SANDBOX=1"}, command...)
	case "kimi":
		// Fennec made the session's working directory: trust it, as Kimi
		// otherwise stops at "Trust this folder?" before the first prompt.
		command = append(command, "--auto")
		command = append([]string{"env", kimiTrustWorkspace}, command...)
	case "opencode":
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		version, err := exec.CommandContext(step, binary, "--version").Output()
		cancel()
		if err != nil {
			return LocalTerminal{}, fmt.Errorf("cannot determine OpenCode permission mode")
		}
		if regexp.MustCompile(`\b2\.`).Match(version) {
			command = append(command, "--auto")
		} else if regexp.MustCompile(`\b1\.`).Match(version) {
			command = append([]string{"env", `OPENCODE_PERMISSION={"*":"allow"}`}, command...)
		} else {
			return LocalTerminal{}, fmt.Errorf("unsupported OpenCode permission mode")
		}
	}
	command = append(command, request.Arguments...)
	if len(request.Hooks) > 0 {
		if err := hookbridge.SaveBindings(runtime.Home, request.ID, request.Hooks); err != nil {
			return LocalTerminal{}, err
		}
	}
	if setup.NativeAgentMode(runtime.Home) {
		// The launcher reads secrets from private files, never command arguments.
		if request.Agent == "claude" || request.Agent == "kimi" {
			command = command[2:]
		}
		if request.Agent == "opencode" && len(command) > 0 && command[0] == "env" {
			command = command[2:]
		}
		command = append([]string{filepath.Join(runtime.Home, ".local/fennec/bin/fennec-terminal"), "native-agent", request.Agent}, command[1:]...)
		if request.Agent == "claude" {
			command = append([]string{"env", "IS_SANDBOX=1"}, command...)
		}
		if request.Agent == "kimi" {
			command = append([]string{"env", kimiTrustWorkspace}, command...)
		}
		if request.Agent == "opencode" {
			command = append([]string{"env", `OPENCODE_PERMISSION={"*":"allow"}`}, command...)
		}
	}
	if request.NativeLogin {
		if err := setup.PrepareNativeLogin(ctx, runtime.Home, setup.NativeLogin{ID: request.ID, Agent: request.Agent}); err != nil {
			return LocalTerminal{}, err
		}
		command = []string{filepath.Join(runtime.Home, ".local/fennec/bin/fennec-terminal"), "native-login", request.Agent, request.ID}
	}
	mark("workspace_prepare")
	if err := ctx.Err(); err != nil {
		return LocalTerminal{}, err
	}
	if err := exclusiveJSON(marker, request); err != nil {
		return LocalTerminal{}, err
	}
	mark("journal_sync")
	arguments := []string{"run", "--bg", "--no-parent", "--outlive-parent", "--json", "--cwd", cwd, "--label", "fennec.session=" + request.ID, "--label", "fennec.agent=" + request.Agent, "--"}
	raw, err = runtime.Run(ctx, append(arguments, command...)...)
	if err != nil {
		return LocalTerminal{}, fmt.Errorf("Herdr launch outcome unknown; inspect session before retrying")
	}
	var terminal LocalTerminal
	if json.Unmarshal(raw, &terminal) != nil || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(terminal.Address) {
		return LocalTerminal{}, fmt.Errorf("invalid launch receipt")
	}
	terminal.ID = request.ID
	terminal.Agent = request.Agent
	mark("herdr_spawn")
	terminal.Timings = timings
	return terminal, nil
}

func exclusiveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("session metadata mismatch")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
