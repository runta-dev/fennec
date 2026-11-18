package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/agentcmd"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Terminal coverage tells the Get started card whether typing a selected
// agent's command in the user's own terminal reaches Fennec. A cloud
// selection without it looks done while the agent still runs locally (the
// 2026-11-18 incident in docs/agent-shims.md).

type agentCoverage struct {
	Coverage string `json:"coverage"`
	Kind     string `json:"kind,omitempty"`
	Path     string `json:"path,omitempty"`
}

type terminalCoverageReply struct {
	Shell     string                   `json:"shell"`
	Agents    map[string]agentCoverage `json:"agents"`
	Snippet   string                   `json:"snippet,omitempty"`
	CheckedAt time.Time                `json:"checked_at"`
}

var (
	// resolveAgentInShell and coverageEvery are vars so tests can replace them.
	resolveAgentInShell = resolveInShell
	coverageEvery       = time.Minute
	coverageCache       struct {
		sync.Mutex
		key   string
		reply terminalCoverageReply
	}
)

func coveredState(coverage string) bool {
	return coverage == "covered_by_function" || coverage == "covered_by_shim" || coverage == "covered_by_entry"
}

// coverageSnippet is the line a user adds to a startup file Fennec may not
// edit (read-only, managed by Nix or a dotfile manager) or for a shell Fennec
// doesn't integrate with.
func coverageSnippet(home, shell string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
	shims := filepath.Join(home, ".local/share/fennec/shims")
	switch filepath.Base(shell) {
	case "zsh", "bash":
		init := filepath.Join(home, ".local/share/fennec/shell-init.sh")
		return "[ -r " + quote(init) + " ] && . " + quote(init)
	case "fish":
		return "fish_add_path --prepend " + quote(shims)
	}
	return `export PATH=` + quote(shims) + `:"$PATH"`
}

// terminalCoverage resolves agents (agent ids; the selected ones when empty)
// in the user's login shell. A result younger than coverageEvery is reused
// unless force: the login shell can take seconds.
func terminalCoverage(ctx context.Context, home string, agents []string, force bool) terminalCoverageReply {
	if len(agents) == 0 {
		enabled, _ := launcher.EnabledAgents(filepath.Join(home, ".config/fennec/takeover.json"))
		for id := range enabled {
			agents = append(agents, id)
		}
	}
	var wanted []string
	for _, id := range agents {
		if launcher.Supported[id] {
			wanted = append(wanted, id)
		}
	}
	sort.Strings(wanted)
	key := strings.Join(wanted, ",")
	coverageCache.Lock()
	defer coverageCache.Unlock()
	if !force && coverageCache.key == key && time.Since(coverageCache.reply.CheckedAt) < coverageEvery {
		return coverageCache.reply
	}
	shell := loginShell(ctx)
	reply := terminalCoverageReply{Shell: shell, Agents: map[string]agentCoverage{}, CheckedAt: time.Now().UTC()}
	uncovered := false
	for _, id := range wanted {
		r := resolveAgentInShell(ctx, shell, agentcmd.BinaryName(id))
		reply.Agents[id] = agentCoverage{Coverage: r.Coverage, Kind: r.Kind, Path: r.Path}
		if !coveredState(r.Coverage) && r.Coverage != "not_installed" && !strings.HasPrefix(r.Coverage, "shadowed_by_") {
			uncovered = true
		}
	}
	if uncovered {
		reply.Snippet = coverageSnippet(home, shell)
	}
	coverageCache.key, coverageCache.reply = key, reply
	return reply
}

func (s *daemonServices) terminalCoverageHandlers() map[string]control.Handler {
	agentsOf := func(raw json.RawMessage) []string {
		var params struct {
			Agents []string `json:"agents"`
			Force  bool     `json:"force"`
		}
		_ = json.Unmarshal(raw, &params)
		return params.Agents
	}
	forced := func(raw json.RawMessage) bool {
		var params struct {
			Force bool `json:"force"`
		}
		_ = json.Unmarshal(raw, &params)
		return params.Force
	}
	return map[string]control.Handler{
		"terminal.coverage": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return terminalCoverage(ctx, s.home, agentsOf(raw), forced(raw)), nil
		},
		// terminal.fix reinstalls the terminal integration where Fennec may
		// write, then checks again; files it may not edit stay as they are.
		"terminal.fix": func(ctx context.Context, raw json.RawMessage) (any, error) {
			executable, err := os.Executable()
			if err != nil {
				return nil, err
			}
			step, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			reconcileTerminalIntegration(step, s.home, executable)
			return terminalCoverage(ctx, s.home, agentsOf(raw), true), nil
		},
	}
}
