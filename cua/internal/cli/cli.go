// Package cli holds the fennec-cua command's testable parts: how the stdio
// bridge starts the daemon, `fennec-cua do` argument parsing and saving the
// captures it receives.
package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/standalone"
)

// AppBundle returns the .app bundle an executable sits in
// (X.app/Contents/...), if any.
func AppBundle(exe string) (string, bool) {
	clean := filepath.Clean(exe)
	parts := strings.Split(clean, string(filepath.Separator))
	for i := len(parts) - 2; i >= 1; i-- {
		if strings.HasSuffix(parts[i], ".app") && parts[i+1] == "Contents" && i+2 < len(parts) {
			b := strings.Join(parts[:i+1], string(filepath.Separator))
			if b == "" {
				b = string(filepath.Separator)
			}
			return b, true
		}
	}
	return "", false
}

// LaunchedByLaunchd reports whether a process with no arguments was started
// by launchd (parent pid 1) from inside an app bundle: LaunchServices
// opening the app, which then serves the daemon in place.
func LaunchedByLaunchd(ppid int, exe string, args []string) bool {
	_, bundled := AppBundle(exe)
	return ppid == 1 && bundled && len(args) == 0
}

// StartCommand is how `fennec-cua mcp` starts a daemon when none answers. A
// binary inside an app bundle opens its app through LaunchServices
// (`open -g -a <bundle>`; the standalone app with -n and the PassedEnv
// settings), so macOS attributes the Accessibility and Screen
// Recording grants to the bundle rather than to the agent's terminal; a bare
// binary runs `serve` in a new session. Either way the daemon holds none of
// the bridge's stdio.
func StartCommand(exe string) *exec.Cmd {
	return startCommand(exe, os.Getenv)
}

func startCommand(exe string, getenv func(string) string) *exec.Cmd {
	var cmd *exec.Cmd
	if bundle, ok := AppBundle(exe); ok && standalone.IsApp(bundle) {
		// The standalone app is the daemon itself: a new instance (another
		// copy serving a different directory must not swallow the start;
		// the daemon lock refuses a duplicate) that gets the settings
		// LaunchServices would not pass on.
		args := []string{"-n", "-g"}
		for _, k := range PassedEnv {
			if v := getenv(k); v != "" {
				args = append(args, "--env", k+"="+v)
			}
		}
		// "serve" says it is a start for an agent: a launch without
		// arguments is the user opening the app, which shows its setup.
		cmd = exec.Command("/usr/bin/open", append(args, "-a", bundle, "--args", "serve")...)
	} else if ok {
		cmd = exec.Command("/usr/bin/open", "-g", "-a", bundle)
	} else {
		cmd = exec.Command(exe, "serve")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	return cmd
}

// Start runs cmd for the bridge: for `open`, its own failure is the start's
// failure and the app then starts the daemon; otherwise exited delivers the
// daemon's exit status should it end.
func Start(cmd *exec.Cmd) (<-chan error, error) {
	if filepath.Base(cmd.Path) == "open" {
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("open the app: %w %s", err, strings.TrimSpace(string(out)))
		}
		return nil, nil
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("exit status 0")
		}
		exited <- err
	}()
	return exited, nil
}

// Embedded reports embedded mode from the environment.
func Embedded() bool { return os.Getenv(mcpserver.EmbeddedEnv) == "1" }

// ParseDoArgs reads `fennec-cua do <tool>` arguments: one JSON object, or
// key=value pairs whose values are JSON when they parse (true, 7, [1]) and
// strings otherwise. A word without '=' is refused.
func ParseDoArgs(args []string) (json.RawMessage, error) {
	if len(args) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(args) == 1 && strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(args[0]), &obj); err != nil {
			return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
		}
		return json.RawMessage(args[0]), nil
	}
	obj := map[string]json.RawMessage{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("argument %q is not key=value (or pass one JSON object)", a)
		}
		if json.Valid([]byte(v)) && strings.TrimSpace(v) != "" {
			obj[k] = json.RawMessage(v)
		} else {
			s, _ := json.Marshal(v)
			obj[k] = s
		}
	}
	data, err := json.Marshal(obj)
	return data, err
}

// SaveCapture writes base64 image data to a new file in dir (the system
// temporary directory when empty) named by the image's real format (png,
// jpg or webp), byte for byte, and returns its path. No caller chooses the
// path.
func SaveCapture(dir, data string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("the capture is not valid base64: %w", err)
	}
	_, ext := mcpserver.SniffImage(raw)
	if ext == "" {
		return "", errors.New("the capture is not a PNG, JPEG or WebP image")
	}
	f, err := os.CreateTemp(dir, "fennec-cua-capture-*."+ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

// Do sends one tool call to the daemon at socket and returns its answer.
// js and js_reset go as they are; any other name is an engine tool, reached
// through the js REPL (the MCP surface offers nothing else) and answered as
// that tool answers.
func Do(ctx context.Context, socket, tool string, args json.RawMessage) (mcpclient.ToolResult, error) {
	c, err := mcpclient.Dial(ctx, socket)
	if err != nil {
		return mcpclient.ToolResult{}, err
	}
	defer c.Close()
	switch tool {
	case "js", "js_reset":
		return c.Tool(ctx, tool, args)
	}
	return c.EngineTool(ctx, tool, args)
}

// PassedEnv are the settings a daemon started through LaunchServices gets
// from the command that started it, so both agree on the daemon directory,
// the configuration and the browser. Secrets (the HTTP token) are never
// passed: `open --env` puts them on a command line.
var PassedEnv = []string{
	"FENNEC_CUA_DIR",
	"FENNEC_CUA_CONFIG",
	"FENNEC_CUA_DRIVER",
	"FENNEC_CUA_CHROMIUM",
	"FENNEC_CUA_CHROMIUM_PROFILE",
	"FENNEC_CUA_CHROMIUM_HEADLESS",
}

func dialUnix(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}
