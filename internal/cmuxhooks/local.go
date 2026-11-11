package cmuxhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const PayloadLimit = 65536

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Response struct {
	Data json.RawMessage `json:"data"`
	Code int             `json:"code"`
}

func Empty() Response { return Response{Data: json.RawMessage(`{}`)} }

type Local struct {
	Executable, Socket, Workspace, Surface string
	Env                                    []string
}

func Capture() *Local {
	workspace, surface := os.Getenv("CMUX_WORKSPACE_ID"), os.Getenv("CMUX_SURFACE_ID")
	if !uuid.MatchString(workspace) || !uuid.MatchString(surface) || os.Getenv("CMUX_CODEX_HOOKS_DISABLED") == "1" {
		return nil
	}
	path := os.Getenv("CMUX_CODEX_HOOK_CMUX_BIN")
	if path == "" {
		path = os.Getenv("CMUX_BUNDLED_CLI_PATH")
	}
	if path == "" {
		path, _ = exec.LookPath("cmux")
	}
	if !filepath.IsAbs(path) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil
	}
	socket := os.Getenv("CMUX_SOCKET_PATH")
	if socket != "" && !filepath.IsAbs(socket) {
		return nil
	}
	return &Local{Executable: path, Socket: socket, Workspace: workspace, Surface: surface, Env: append([]string(nil), os.Environ()...)}
}
func Timeout(event string) time.Duration {
	if event == "notification" {
		return 110 * time.Second
	}
	return 4 * time.Second
}
func (local *Local) Run(ctx context.Context, event string, payload []byte) Response {
	if local == nil || !ValidEvent(event) || len(payload) > PayloadLimit || !json.Valid(payload) {
		return Empty()
	}
	// Relay-origin is an official cmux mode: remote PIDs/transcripts must never
	// be used to infer a local process or read a local transcript file.
	var input map[string]json.RawMessage
	if json.Unmarshal(payload, &input) != nil || input == nil {
		return Empty()
	}
	for _, key := range []string{"transcript_path", "transcriptPath", "cwd"} {
		delete(input, key)
	}
	var sanitized bytes.Buffer
	encoder := json.NewEncoder(&sanitized)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(input) != nil || sanitized.Len() > PayloadLimit {
		return Empty()
	}
	payload = sanitized.Bytes()
	step, cancel := context.WithTimeout(ctx, Timeout(event))
	defer cancel()
	args := []string{}
	if local.Socket != "" {
		args = append(args, "--socket", local.Socket)
	}
	args = append(args, "hooks", "codex", event, "--workspace", local.Workspace, "--surface", local.Surface)
	command := exec.CommandContext(step, local.Executable, args...)
	command.WaitDelay = 250 * time.Millisecond
	env := []string{}
	for _, value := range local.Env {
		key, _, _ := strings.Cut(value, "=")
		if key != "CMUX_AGENT_HOOK_RELAY_ORIGIN" && key != "CMUX_CODEX_PID" && key != "CMUX_CODEX_HOOK_PID" {
			env = append(env, value)
		}
	}
	command.Env = append(env, "CMUX_AGENT_HOOK_RELAY_ORIGIN=1")
	command.Stdin = bytes.NewReader(payload)
	output := &boundedOutput{}
	command.Stdout = output
	err := command.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 {
			return Empty()
		}
		code = 2
	}
	data := bytes.TrimSpace(output.Bytes())
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	if !json.Valid(data) {
		return Empty()
	}
	return Response{Data: append([]byte(nil), data...), Code: code}
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > PayloadLimit {
		return 0, fmt.Errorf("hook output exceeds limit")
	}
	return b.Buffer.Write(data)
}
