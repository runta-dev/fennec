package localssh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fennec/internal/audit"
	"fennec/internal/guardian"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/gliderlabs/ssh"
)

// DesktopMCPCommand starts one of ChatGPT.app's own MCP servers (node_repl,
// cua_repl) on this Mac for a cloud ChatGPT session. These tools drive the
// Mac itself, so they run here rather than on the cloud computer. Only
// executables inside the ChatGPT or Codex application bundle are accepted, and
// the server lives as long as the SSH session instead of the ten-minute exec
// limit.
const DesktopMCPCommand = "fennec-desktop-mcp"

// DesktopMCPSpec is the MCP server definition ChatGPT registered, carried
// unchanged from the cloud request.
type DesktopMCPSpec struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

var (
	desktopMCPRoots = []string{"/Applications/ChatGPT.app/Contents/", "/Applications/Codex.app/Contents/"}
	desktopMCPName  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	desktopMCPEnv   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// EncodeDesktopMCP returns the remote command for an MCP server definition.
func EncodeDesktopMCP(spec DesktopMCPSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	command := DesktopMCPCommand + " " + base64.RawURLEncoding.EncodeToString(data)
	if len(command) > 8192 {
		return "", fmt.Errorf("desktop MCP definition too large")
	}
	return command, nil
}

// IsDesktopMCPCommand reports executables that belong to ChatGPT.app.
func IsDesktopMCPCommand(command string) bool {
	for _, root := range desktopMCPRoots {
		if strings.HasPrefix(command, root) {
			return true
		}
	}
	return false
}

func parseDesktopMCP(command string) (DesktopMCPSpec, string, bool, error) {
	encoded, ok := strings.CutPrefix(command, DesktopMCPCommand+" ")
	if !ok {
		return DesktopMCPSpec{}, "", false, nil
	}
	var spec DesktopMCPSpec
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || json.Unmarshal(data, &spec) != nil {
		return spec, "", true, fmt.Errorf("invalid desktop MCP definition")
	}
	if !desktopMCPName.MatchString(spec.Name) || !filepath.IsAbs(spec.Command) || len(spec.Args) > 64 || len(spec.Env) > 128 {
		return spec, "", true, fmt.Errorf("invalid desktop MCP definition")
	}
	for _, arg := range spec.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return spec, "", true, fmt.Errorf("invalid desktop MCP argument")
		}
	}
	for key, value := range spec.Env {
		if !desktopMCPEnv.MatchString(key) || len(value) > 8192 || strings.ContainsRune(value, 0) {
			return spec, "", true, fmt.Errorf("invalid desktop MCP environment")
		}
	}
	resolved, err := filepath.EvalSymlinks(spec.Command)
	if err != nil || !IsDesktopMCPCommand(resolved) {
		return spec, "", true, fmt.Errorf("desktop MCP servers must be ChatGPT.app executables")
	}
	return spec, resolved, true, nil
}

// serveDesktopMCP runs an approved ChatGPT desktop MCP server with the SSH
// session as its stdio. It reports whether the command was a desktop MCP
// request; other commands continue through the normal exec path.
func serveDesktopMCP(operation context.Context, session ssh.Session, broker *Broker, command, fingerprint string, requireApproval bool) bool {
	spec, executable, ok, err := parseDesktopMCP(command)
	if !ok {
		return false
	}
	display := "ChatGPT desktop tool " + spec.Name
	record := func(decision string, blocked bool, code int, detail string, started time.Time) {
		broker.recordAudit(audit.Entry{Kind: "exec", Command: display, Workdir: spec.Cwd, SandboxMode: "full", NetworkAllowed: true, Decision: decision, Blocked: blocked, ExitCode: code, DurationMS: time.Since(started).Milliseconds(), Detail: detail})
	}
	started := time.Now()
	if err != nil {
		io.WriteString(session.Stderr(), "Fennec: "+err.Error()+"\n")
		record(decisionForbidden, true, 126, err.Error(), started)
		session.Exit(126)
		return true
	}
	decision := "auto_sandbox"
	// ChatGPT starts these tools for every conversation it opens; one approval
	// per tool covers the daemon run, and each start is still audited.
	capability := DesktopMCPCommand + " " + spec.Name
	if requireApproval && broker.granted(fingerprint, capability) {
		decision = "previously_approved"
	} else if requireApproval {
		approval, cancel := context.WithTimeout(operation, 2*time.Minute)
		outcome := broker.authorize(approval, approvalRequest{Display: display, Fingerprint: fingerprint, Action: guardian.Action{Kind: "exec", Command: display, Cwd: spec.Cwd, Justification: "ChatGPT on the cloud computer uses its " + spec.Name + " tool on this Mac"}})
		cancel()
		if !outcome.Allowed {
			io.WriteString(session.Stderr(), "Fennec: "+spec.Name+" was not approved on this Mac.\n")
			record(outcome.Decision, true, 126, "Denied or expired during approval prompt", started)
			session.Exit(126)
			return true
		}
		broker.grant(fingerprint, capability)
		decision = outcome.Decision
	}
	home, _ := os.UserHomeDir()
	cmd := exec.CommandContext(operation, executable, spec.Args...)
	cmd.Dir = home
	if info, err := os.Stat(spec.Cwd); err == nil && info.IsDir() {
		cmd.Dir = spec.Cwd
	}
	cmd.Env = os.Environ()
	for key, value := range spec.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = session, session, session.Stderr()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	code := 0
	if err := cmd.Run(); err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			code = exit.ExitCode()
		}
	}
	record(decision, false, code, "", started)
	session.Exit(code)
	return true
}
