package cmuxhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/hookbridge"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

var claudeEvents = map[string][]string{
	"SessionStart": {"session-start", "inbox-wait"}, "SessionEnd": {"session-end"},
	"Stop": {"stop", "feed", "auto-name", "inbox-wait"}, "StopFailure": {"stop", "inbox-wait"},
	"Notification": {"notification"}, "PermissionRequest": {"feed"},
	"PreToolUse": {"cron-create-guard", "pre-tool-use"}, "PostToolUse": {"push-notification"},
	"SubagentStop": {"feed"}, "UserPromptSubmit": {"prompt-submit", "inbox-drain"},
}

func MatchClaude(event, command string) (string, error) {
	sub := claudeCommands[hookbridge.Digest([]byte(command))]
	if sub != "" {
		for _, allowed := range claudeEvents[event] {
			if sub == allowed {
				return event + "/" + sub, nil
			}
		}
	}
	if strings.Contains(command, "CMUX_CLAUDE_") {
		return "", fmt.Errorf("this cmux Claude hook is not supported; update Fennec before launching")
	}
	return "", nil
}

type ClaudeAdapter struct {
	Local          *Local
	Event, Command string
	Ref            hookbridge.Binding
}

func ClaudeAdapters(local *Local) (map[string]*ClaudeAdapter, error) {
	result := map[string]*ClaudeAdapter{}
	if local == nil {
		return result, nil
	}
	if !ValidContext(local) {
		return nil, fmt.Errorf("invalid cmux context")
	}
	local = cleanClaudeLocal(local)
	hash, err := hookbridge.FileDigest(local.Executable)
	if err != nil {
		return nil, err
	}
	for event, commands := range claudeEvents {
		for _, command := range commands {
			policy := hookbridge.Observer
			if event == "PermissionRequest" || command == "cron-create-guard" {
				policy = hookbridge.Required
			}
			timeout := 4 * time.Second
			if command == "feed" && event == "PermissionRequest" || command == "auto-name" || command == "inbox-wait" {
				timeout = 110 * time.Second
			}
			a := &ClaudeAdapter{Local: local, Event: event, Command: command, Ref: hookbridge.Binding{Name: "cmux · Claude · " + command, Event: event, Policy: policy, TimeoutMS: int(timeout.Milliseconds())}}
			a.Ref.ID = a.fingerprint(hash)
			result[event+"/"+command] = a
		}
	}
	return result, nil
}

func cleanClaudeLocal(local *Local) *Local {
	local = cleanLocal(local)
	var env []string
	for _, entry := range local.Env {
		key, value, _ := strings.Cut(entry, "=")
		// The wrapper exports these only for the native child. Fixed CLI hooks
		// use neither its shell spool nor Node preload/CUA configuration. Keeping
		// them would change binding IDs when plain `fen attach` reconnects.
		if key == "CMUX_CLAUDE_HOOK_CMUX_BIN" || key == "CMUX_CLAUDE_HOOK_SPOOL_DIR" || strings.HasPrefix(key, "CMUX_ORIGINAL_NODE_OPTIONS") || strings.HasPrefix(key, "CMUX_CUA_") || key == "CMUX_CLAUDE_HEADLESS" && value == "0" {
			continue
		}
		env = append(env, entry)
	}
	local.Env = env
	return local
}
func (a *ClaudeAdapter) fingerprint(hash string) string {
	// The shared adapter already filters per-invocation routing data out of grants.
	base := (&Adapter{Local: a.Local, Event: "session-start"}).fingerprintWithHash(hash)
	raw, _ := json.Marshal([]any{"cmux-claude-v1", base, a.Event, a.Command, a.Ref.Policy, a.Ref.TimeoutMS})
	return hookbridge.Digest(raw)
}
func (a *ClaudeAdapter) Binding() hookbridge.Binding { return a.Ref }
func (a *ClaudeAdapter) Fingerprint() (string, error) {
	hash, err := hookbridge.FileDigest(a.Local.Executable)
	if err != nil {
		return "", err
	}
	return a.fingerprint(hash), nil
}
func (a *ClaudeAdapter) Description() string {
	return "cmux · Claude · " + a.Command + "\n" + a.Local.Executable + "\nWorkspace: " + a.Local.Workspace + "\nSurface: " + a.Local.Surface
}
func (a *ClaudeAdapter) Execute(ctx context.Context, payload []byte) hookbridge.Result {
	var input map[string]json.RawMessage
	if len(payload) > PayloadLimit || json.Unmarshal(payload, &input) != nil || input == nil {
		return hookbridge.Failure(a.Ref, "received invalid input")
	}
	for _, key := range []string{"transcript_path", "transcriptPath", "cwd"} {
		delete(input, key)
	}
	payload, _ = json.Marshal(input)
	args := []string{}
	if a.Local.Socket != "" {
		args = append(args, "--socket", a.Local.Socket)
	}
	if a.Command == "feed" {
		args = append(args, "hooks", "feed", "--source", "claude")
	} else {
		args = append(args, "hooks", "claude", a.Command)
	}
	args = append(args, "--workspace", a.Local.Workspace, "--surface", a.Local.Surface)
	cmd := exec.CommandContext(ctx, a.Local.Executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 250 * time.Millisecond
	for _, entry := range a.Local.Env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasSuffix(key, "_PID") || key == "CMUX_AGENT_HOOK_RELAY_ORIGIN" || key == "CMUX_CLAUDE_HOOK_SPOOL_DIR" || key == "CMUX_WORKSPACE_ID" || key == "CMUX_SURFACE_ID" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "CMUX_AGENT_HOOK_RELAY_ORIGIN=1", "CMUX_WORKSPACE_ID="+a.Local.Workspace, "CMUX_SURFACE_ID="+a.Local.Surface)
	cmd.Stdin = bytes.NewReader(payload)
	stdout, stderr := &boundedOutput{}, &boundedOutput{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 {
			return hookbridge.Failure(a.Ref, "is unavailable")
		}
		code = 2
	}
	if stdout.Len()+stderr.Len() > PayloadLimit {
		return hookbridge.Failure(a.Ref, "output exceeded limit")
	}
	return hookbridge.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Code: code, Status: "executed"}
}
