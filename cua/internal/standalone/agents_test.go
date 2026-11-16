package standalone

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgents is a home with this app, the command link and fake claude and
// codex commands that edit their configs the way the real ones do.
type fakeAgents struct {
	env   *AgentEnv
	app   string
	calls []string
	fail  string // a call that fails, as "claude mcp add"
}

func newFakeAgents(t *testing.T) *fakeAgents {
	t.Helper()
	home := t.TempDir()
	app := fakeApp(t, filepath.Join(home, "Applications"), "0.1.2")
	f := &fakeAgents{app: app}
	link := DefaultLayout(home).Link()
	f.env = &AgentEnv{
		Home: home, Command: link, SkillSrc: SkillDir(app),
		Getenv: func(string) string { return "" },
		Find:   func(name string) string { return "/fake/" + name },
	}
	f.env.Run = func(_ context.Context, path string, args ...string) (string, error) {
		call := filepath.Base(path) + " " + strings.Join(args, " ")
		f.calls = append(f.calls, call)
		// The real codex fails while CODEX_HOME is missing.
		if _, err := os.Stat(f.env.configDir(filepath.Base(path))); err != nil {
			return "failed to resolve CODEX_HOME", errors.New("exit status 1")
		}
		if f.fail != "" && strings.HasPrefix(call, f.fail) {
			return "boom", errors.New("exit status 1")
		}
		id := filepath.Base(path)
		cmd := ""
		if args[1] == "add" {
			cmd = args[len(args)-2]
		}
		f.setMCP(t, id, cmd)
		return "", nil
	}
	return f
}

func (f *fakeAgents) setMCP(t *testing.T, id, command string) {
	t.Helper()
	switch id {
	case "claude":
		cfg := map[string]any{"projects": map[string]any{}}
		if command != "" {
			cfg["mcpServers"] = map[string]any{MCPName: map[string]any{"type": "stdio", "command": command, "args": []string{"mcp"}}}
		}
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(filepath.Join(f.env.Home, ".claude.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	case "codex":
		dir := filepath.Join(f.env.Home, ".codex")
		_ = os.MkdirAll(dir, 0o755)
		text := "model = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"x\"\n"
		if command != "" {
			data, _ := json.Marshal(command)
			text += "\n[mcp_servers.fennec-cua]\ncommand = " + string(data) + "\nargs = [\"mcp\"]\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectFresh(t *testing.T) {
	f := newFakeAgents(t)
	for _, a := range Agents {
		if s := f.env.Status(a); !s.Installed || s.Connected || s.MCP != LinkNone || s.Skill != LinkNone {
			t.Fatalf("%s before: %+v", a.ID, s)
		}
		s := f.env.Connect(context.Background(), a)
		if !s.Connected || s.Error != "" {
			t.Fatalf("%s after: %+v", a.ID, s)
		}
		if dest, _ := os.Readlink(f.env.SkillTarget(a.ID)); dest != f.env.SkillSrc {
			t.Errorf("%s skill -> %s", a.ID, dest)
		}
	}
	want := []string{
		"claude mcp add --scope user fennec-cua -- " + f.env.Command + " mcp",
		"codex mcp add fennec-cua -- " + f.env.Command + " mcp",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
	// Connecting again changes nothing.
	f.calls = nil
	for _, a := range Agents {
		if s := f.env.Connect(context.Background(), a); !s.Connected {
			t.Fatalf("%s again: %+v", a.ID, s)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("reconnect ran %v", f.calls)
	}
}

// 0.1.1 users ran the copied command: an entry pointing into the app bundle
// and a copied skill. Connect replaces both with this app's.
func TestConnectReplacesOurs(t *testing.T) {
	f := newFakeAgents(t)
	claude, _ := AgentByID("claude")
	f.setMCP(t, "claude", "/Applications/Old Place/FennecCUA.app/Contents/MacOS/fennec-cua")
	if err := CopyTree(f.env.SkillSrc, f.env.SkillTarget("claude")); err != nil {
		t.Fatal(err)
	}
	s := f.env.Status(claude)
	if s.MCP != LinkOurs || s.Skill != LinkOurs {
		t.Fatalf("before: %+v", s)
	}
	if s = f.env.Connect(context.Background(), claude); !s.Connected {
		t.Fatalf("after: %+v", s)
	}
	if !strings.HasPrefix(f.calls[0], "claude mcp remove --scope user fennec-cua") {
		t.Errorf("calls %v", f.calls)
	}
}

// Someone else's fennec-cua server or skill directory is never touched.
func TestConnectLeavesOthers(t *testing.T) {
	f := newFakeAgents(t)
	codex, _ := AgentByID("codex")
	f.setMCP(t, "codex", "/usr/local/bin/some-other-server")
	target := f.env.SkillTarget("codex")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := f.env.Connect(context.Background(), codex)
	if s.Connected || s.MCP != LinkOther || s.Skill != LinkOther || s.Error == "" {
		t.Fatalf("%+v", s)
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v", f.calls)
	}
	if data, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); !strings.Contains(string(data), "mine") {
		t.Error("the other skill was changed")
	}
}

func TestConnectFailureReported(t *testing.T) {
	f := newFakeAgents(t)
	f.fail = "claude mcp add"
	claude, _ := AgentByID("claude")
	s := f.env.Connect(context.Background(), claude)
	if s.Connected || !strings.Contains(s.Error, "boom") || !strings.Contains(s.Manual, "claude mcp add") {
		t.Fatalf("%+v", s)
	}
}

func TestNotInstalled(t *testing.T) {
	f := newFakeAgents(t)
	f.env.Find = func(string) string { return "" }
	codex, _ := AgentByID("codex")
	if s := f.env.Connect(context.Background(), codex); s.Installed || s.Connected || len(f.calls) != 0 {
		t.Fatalf("%+v %v", s, f.calls)
	}
}

func TestDisconnect(t *testing.T) {
	f := newFakeAgents(t)
	for _, a := range Agents {
		f.env.Connect(context.Background(), a)
		s := f.env.Disconnect(context.Background(), a)
		if s.MCP != LinkNone || s.Skill != LinkNone || s.Error != "" {
			t.Fatalf("%s: %+v", a.ID, s)
		}
	}
	// The app's own skill is untouched.
	if _, err := os.Stat(filepath.Join(f.env.SkillSrc, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCodexQuotedTable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[mcp_servers.\"fennec-cua\"]\ncommand = '/a b/fennec-cua'\n[x]\ncommand = \"no\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fh, _ := os.Open(p)
	defer fh.Close()
	if got, err := codexMCPCommand(fh); err != nil || got != "/a b/fennec-cua" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSearchPath(t *testing.T) {
	p := SearchPath("/h", "/a:/b", "/b:/c")
	if !strings.HasPrefix(p, "/a:/b:/h/.local/bin:") || strings.Count(p, "/b:") != 1 || !strings.Contains(p, ":/c:") {
		t.Fatal(p)
	}
}

// Fennec's launchers hand claude and codex to Fennec (maybe to the cloud):
// connect runs the local command they wrap instead.
func TestFennecLauncherTarget(t *testing.T) {
	dir := t.TempDir()
	write := func(p, text string, mode os.FileMode) string {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	launcher := func(orig string) string {
		return "#!/bin/sh\n# Fennec managed launcher v1\nif [ ! -e '/Applications/Fennec.app/Contents/MacOS/fen' ]; then\n if [ -x '" + orig + "' ]; then\n  exec '" + orig + "' \"$@\"\n fi\n exit 127\nfi\nexec '/Applications/Fennec.app/Contents/MacOS/fen' dispatch-agent 'claude' \"$@\"\n"
	}
	codexJS := write(filepath.Join(dir, "lib", "codex.js"), "#!/usr/bin/env node\n", 0o755)
	if got, ok := FennecLauncherTarget(write(filepath.Join(dir, "shims", "codex"), launcher(codexJS), 0o755)); !ok || got != codexJS {
		t.Errorf("codex: %q %v", got, ok)
	}
	// The wrapped claude version was updated away: the newest one stands in.
	versions := filepath.Join(dir, "claude", "versions")
	newest := write(filepath.Join(versions, "2.1.325"), "bin", 0o755)
	if got, ok := FennecLauncherTarget(write(filepath.Join(dir, "shims", "claude"), launcher(filepath.Join(versions, "2.1.318")), 0o755)); !ok || got != newest {
		t.Errorf("claude: %q %v", got, ok)
	}
	if got, ok := FennecLauncherTarget(write(filepath.Join(dir, "shims", "gone"), launcher(filepath.Join(dir, "nowhere", "x")), 0o755)); !ok || got != "" {
		t.Errorf("gone: %q %v", got, ok)
	}
	if _, ok := FennecLauncherTarget(write(filepath.Join(dir, "bin", "real"), "#!/bin/sh\necho hi\n", 0o755)); ok {
		t.Error("a plain script is taken for a launcher")
	}
}
