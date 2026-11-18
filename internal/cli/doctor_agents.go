package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/agentcmd"
	"fennec/internal/launcher"
	"fennec/internal/setup"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// agentResolution is how one shell resolves one agent command.
type agentResolution struct {
	Command        string `json:"command"`
	Kind           string `json:"kind"`
	Path           string `json:"path,omitempty"`
	Ours           bool   `json:"ours"`
	NonInteractive string `json:"non_interactive,omitempty"`
	Coverage       string `json:"coverage"`
}

// resolveInShell asks the user's login shell, interactively, how it resolves
// name, outside any Fennec relay or dispatch marker.
func resolveInShell(ctx context.Context, shell, name string) agentResolution {
	r := agentResolution{Command: name}
	var script string
	switch filepath.Base(shell) {
	case "zsh":
		script = `w=$(whence -w ` + name + `); echo "kind:${w##*: }"; echo "path:$(whence -p ` + name + `)"; functions ` + name + ` 2>/dev/null | grep -q dispatch-stable && echo ours:1`
	case "bash":
		script = `echo "kind:$(type -t ` + name + `)"; echo "path:$(type -P ` + name + `)"; declare -f ` + name + ` | grep -q dispatch-stable && echo ours:1`
	case "fish":
		script = `echo kind:(type -t ` + name + ` 2>/dev/null); echo path:(command -v ` + name + `); functions ` + name + ` 2>/dev/null | string match -q '*dispatch-stable*'; and echo ours:1`
	default:
		r.Coverage = "unsupported_shell"
		return r
	}
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, launcher.RelayEnv+"=") && !strings.HasPrefix(value, launcher.DispatchedEnv+"=") {
			env = append(env, value)
		}
	}
	run := func(args ...string) []byte {
		step, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(step, shell, args...)
		cmd.Env = env
		cmd.Stdin = nil
		out, _ := cmd.Output()
		return out
	}
	scanner := bufio.NewScanner(bytes.NewReader(run("-i", "-l", "-c", script)))
	for scanner.Scan() {
		line := scanner.Text()
		if v, ok := strings.CutPrefix(line, "kind:"); ok {
			r.Kind = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "path:"); ok {
			r.Path = strings.TrimSpace(v)
		} else if line == "ours:1" {
			r.Ours = true
		}
	}
	r.NonInteractive = strings.TrimSpace(string(run("-c", "command -v "+name)))
	home, _ := os.UserHomeDir()
	shims := filepath.Join(home, ".local/share/fennec/shims")
	managed := func(path string) bool {
		data, err := os.ReadFile(path)
		return err == nil && (bytes.Contains(data[:min(len(data), 128)], []byte("# Fennec managed entry v1")) || bytes.Contains(data[:min(len(data), 128)], []byte("# Fennec managed launcher v1")))
	}
	switch {
	case r.Kind == "alias":
		r.Coverage = "shadowed_by_alias"
	case r.Kind == "function" && r.Ours:
		r.Coverage = "covered_by_function"
	case r.Kind == "function":
		r.Coverage = "shadowed_by_function"
	case r.Path == "":
		r.Coverage = "not_installed"
	case filepath.Dir(r.Path) == shims:
		r.Coverage = "covered_by_shim"
	case managed(r.Path):
		r.Coverage = "covered_by_entry"
	default:
		r.Coverage = "not_covered"
	}
	return r
}

// fenDoctorAgents prints how this Mac's terminals reach each agent, without
// secrets, so a coverage problem needs one command to diagnose.
func fenDoctorAgents(arguments []string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	shell := loginShell(ctx)
	zdot, xdg := setup.LocalShellDirectories(ctx)
	enabled, _ := launcher.EnabledAgents(filepath.Join(home, ".config/fennec/takeover.json"))
	var report json.RawMessage
	if data, err := os.ReadFile(terminalIntegrationPath(home)); err == nil {
		report = data
	}
	names := []string{}
	for id := range launcher.Supported {
		names = append(names, id)
	}
	sort.Strings(names)
	results := []agentResolution{}
	for _, id := range names {
		results = append(results, resolveInShell(ctx, shell, agentcmd.BinaryName(id)))
	}
	if len(arguments) > 0 && arguments[0] == "--json" {
		return json.NewEncoder(output).Encode(map[string]any{
			"login_shell": shell, "zdotdir": zdot, "xdg_config_home": xdg,
			"application": launcher.RegisteredExecutable(home), "application_present": launcher.ApplicationPresent(home),
			"stable_dispatcher": launcher.StableExecutable(home), "selected": enabled,
			"last_reconcile": report, "agents": results,
		})
	}
	fmt.Fprintf(output, "Login shell: %s\nZDOTDIR: %s\nXDG_CONFIG_HOME: %s\n", shell, zdot, xdg)
	fmt.Fprintf(output, "Application: %s (present: %t)\n", launcher.RegisteredExecutable(home), launcher.ApplicationPresent(home))
	if _, err := os.Stat(launcher.StableExecutable(home)); err == nil {
		fmt.Fprintf(output, "Stable dispatcher: %s\n", launcher.StableExecutable(home))
	} else {
		fmt.Fprintf(output, "Stable dispatcher: missing\n")
	}
	if len(report) > 0 {
		fmt.Fprintf(output, "Last reconcile: %s\n", strings.TrimSpace(string(report)))
	} else {
		fmt.Fprintf(output, "Last reconcile: none recorded\n")
	}
	fmt.Fprintf(output, "\n%-14s %-9s %-22s %-10s %s\n", "COMMAND", "SELECTED", "COVERAGE", "KIND", "PATH / NON-INTERACTIVE")
	for i, id := range names {
		r := results[i]
		fmt.Fprintf(output, "%-14s %-9t %-22s %-10s %s | %s\n", r.Command, enabled[id], r.Coverage, r.Kind, r.Path, r.NonInteractive)
	}
	return nil
}
