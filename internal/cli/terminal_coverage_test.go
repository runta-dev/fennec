package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeCoverage(t *testing.T, result func(name string) agentResolution) *int {
	t.Helper()
	calls := 0
	oldResolve, oldEvery := resolveAgentInShell, coverageEvery
	resolveAgentInShell = func(_ context.Context, _, name string) agentResolution {
		calls++
		r := result(name)
		r.Command = name
		return r
	}
	coverageCache.Lock()
	coverageCache.key, coverageCache.reply = "", terminalCoverageReply{}
	coverageCache.Unlock()
	t.Cleanup(func() { resolveAgentInShell, coverageEvery = oldResolve, oldEvery })
	return &calls
}

// Only the agents asked for are resolved, by command name (cursor-agent for
// cursor), and a covered agent needs no snippet.
func TestTerminalCoverageReportsTheAgentsAsked(t *testing.T) {
	calls := fakeCoverage(t, func(name string) agentResolution {
		return agentResolution{Coverage: "covered_by_function", Kind: "function", Path: "/x/" + name}
	})
	reply := terminalCoverage(context.Background(), t.TempDir(), []string{"cursor", "vim"}, true)
	if *calls != 1 || reply.Agents["cursor"].Path != "/x/cursor-agent" || reply.Snippet != "" || len(reply.Agents) != 1 {
		t.Fatalf("calls %d reply %+v", *calls, reply)
	}
}

// The login shell is asked at most once a minute for the same agents; force
// (after Fix) and a different selection ask again.
func TestTerminalCoverageIsRateLimited(t *testing.T) {
	calls := fakeCoverage(t, func(string) agentResolution { return agentResolution{Coverage: "covered_by_shim"} })
	home := t.TempDir()
	terminalCoverage(context.Background(), home, []string{"codex"}, false)
	terminalCoverage(context.Background(), home, []string{"codex"}, false)
	if *calls != 1 {
		t.Fatalf("asked %d times within the minute", *calls)
	}
	terminalCoverage(context.Background(), home, []string{"codex"}, true)
	terminalCoverage(context.Background(), home, []string{"claude"}, false)
	if *calls != 3 {
		t.Fatalf("asked %d times, want 3", *calls)
	}
	coverageEvery = time.Nanosecond
	terminalCoverage(context.Background(), home, []string{"claude"}, false)
	if *calls != 4 {
		t.Fatalf("an old result was reused")
	}
}

// With no agents named, the selected ones are checked.
func TestTerminalCoverageDefaultsToTheSelection(t *testing.T) {
	calls := fakeCoverage(t, func(string) agentResolution { return agentResolution{Coverage: "covered_by_entry"} })
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".config/fennec"), 0o700)
	os.WriteFile(filepath.Join(home, ".config/fennec/takeover.json"), []byte(`{"agents":["claude","codex"]}`), 0o600)
	reply := terminalCoverage(context.Background(), home, nil, true)
	if *calls != 2 || len(reply.Agents) != 2 {
		t.Fatalf("calls %d reply %+v", *calls, reply)
	}
}

// An uncovered agent gets the line to add; one shadowed by the user's own
// alias does not (that is their choice), nor one not installed.
func TestTerminalCoverageSnippet(t *testing.T) {
	coverage := map[string]string{"codex": "not_covered", "claude": "shadowed_by_alias", "pi": "not_installed"}
	fakeCoverage(t, func(name string) agentResolution { return agentResolution{Coverage: coverage[name]} })
	home := t.TempDir()
	if r := terminalCoverage(context.Background(), home, []string{"claude", "pi"}, true); r.Snippet != "" {
		t.Fatalf("snippet for shadowed/not installed: %q", r.Snippet)
	}
	if r := terminalCoverage(context.Background(), home, []string{"codex"}, true); r.Snippet == "" {
		t.Fatal("no snippet for an uncovered agent")
	}
	for shell, want := range map[string]string{"/bin/zsh": "/.local/share/fennec/shell-init.sh' ] && . '", "/opt/homebrew/bin/fish": "fish_add_path --prepend '", "/usr/local/bin/nu": `export PATH='`} {
		if got := coverageSnippet(home, shell); !strings.Contains(got, want) || !strings.Contains(got, home) {
			t.Errorf("%s: %q", shell, got)
		}
	}
}
