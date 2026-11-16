package standalone

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MCPName is the name fennec-cua registers under in an agent's MCP servers.
const MCPName = "fennec-cua"

// Agent is a coding agent the setup window connects: its MCP server entry
// and the skill.
type Agent struct {
	ID   string // "claude", "codex"
	Name string // shown to the user
	CLI  string // its command
}

// Agents are the agents connect knows, in the order the setup window lists
// them.
var Agents = []Agent{
	{ID: "claude", Name: "Claude Code", CLI: "claude"},
	{ID: "codex", Name: "Codex", CLI: "codex"},
}

// Link states of an MCP entry or a skill.
const (
	LinkNone    = "none"    // absent
	LinkCurrent = "current" // ours, pointing at this app
	LinkOurs    = "ours"    // ours, pointing at another or a removed copy: replaced on connect
	LinkOther   = "other"   // someone else's under our name: never touched
)

// AgentStatus is one agent as `fennec-cua agents --json` reports it.
type AgentStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CLI       string `json:"cli,omitempty"` // the command found, "" when not installed
	Installed bool   `json:"installed"`
	MCP       string `json:"mcp"`
	Skill     string `json:"skill"`
	Connected bool   `json:"connected"`
	// Error is what the last connect or disconnect could not do.
	Error string `json:"error,omitempty"`
	// Manual is the command that does the same by hand, for when connecting
	// failed.
	Manual string `json:"manual,omitempty"`
}

// AgentEnv is what connecting reads and runs; tests replace the commands.
type AgentEnv struct {
	Home string
	// Command is the fennec-cua the MCP entry starts (the ~/.local/bin link
	// when it leads to this app).
	Command string
	// SkillSrc is the skill inside the app, which the agents' skill entries
	// link to, so an app update updates the skill.
	SkillSrc string
	// Getenv reads CLAUDE_CONFIG_DIR and CODEX_HOME; os.Getenv by default.
	Getenv func(string) string
	// Find returns an agent's command path, "" when it is not installed.
	Find func(name string) string
	// Run runs an agent's command and returns its combined output.
	Run func(ctx context.Context, path string, args ...string) (string, error)
}

func (e *AgentEnv) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.Getenv(k)
}

// configDir is the agent's own directory (~/.claude, ~/.codex).
func (e *AgentEnv) configDir(id string) string {
	switch id {
	case "claude":
		if d := e.getenv("CLAUDE_CONFIG_DIR"); d != "" {
			return d
		}
		return filepath.Join(e.Home, ".claude")
	case "codex":
		if d := e.getenv("CODEX_HOME"); d != "" {
			return d
		}
		return filepath.Join(e.Home, ".codex")
	}
	return ""
}

// SkillTarget is where an agent loads the skill from.
func (e *AgentEnv) SkillTarget(id string) string {
	return filepath.Join(e.configDir(id), "skills", SkillName)
}

// mcpCommand is the command of the agent's fennec-cua MCP entry, "" when it
// has none. Claude Code keeps user-scope servers in ~/.claude.json (in
// CLAUDE_CONFIG_DIR when set), Codex in config.toml.
func (e *AgentEnv) mcpCommand(id string) (string, error) {
	switch id {
	case "claude":
		path := filepath.Join(e.Home, ".claude.json")
		if d := e.getenv("CLAUDE_CONFIG_DIR"); d != "" {
			path = filepath.Join(d, ".claude.json")
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		var cfg struct {
			MCPServers map[string]struct {
				Command string `json:"command"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if s, ok := cfg.MCPServers[MCPName]; ok {
			if s.Command == "" {
				return "?", nil
			}
			return s.Command, nil
		}
		return "", nil
	case "codex":
		path := filepath.Join(e.configDir("codex"), "config.toml")
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		defer f.Close()
		return codexMCPCommand(f)
	}
	return "", nil
}

// codexMCPCommand finds command in config.toml's [mcp_servers.fennec-cua]
// table; "?" when the table has no command it can read.
func codexMCPCommand(f *os.File) (string, error) {
	in := false
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			h := strings.TrimSpace(strings.Trim(line, "[]"))
			in = h == "mcp_servers."+MCPName || h == `mcp_servers."`+MCPName+`"`
			found = found || in
			continue
		}
		if !in || !strings.HasPrefix(line, "command") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "command" {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "'") {
			if end := strings.Index(v[1:], "'"); end >= 0 {
				return v[1 : 1+end], nil
			}
		}
		if s, err := strconv.QuotedPrefix(v); err == nil {
			if u, err := strconv.Unquote(s); err == nil {
				return u, nil
			}
		}
		return "?", nil
	}
	if found {
		return "?", sc.Err()
	}
	return "", sc.Err()
}

// ourBinary says whether path is a fennec-cua of this app's kind: the
// command link, or fennec-cua inside a FennecCUA app bundle (an app that is
// gone counts: a removed or ejected copy).
func (e *AgentEnv) ourBinary(path string) bool {
	if path == e.Command {
		return true
	}
	if filepath.Base(path) != BinName {
		return false
	}
	if path == DefaultLayout(e.Home).Link() {
		return true
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if filepath.Ext(app) != ".app" || filepath.Base(filepath.Dir(path)) != "MacOS" {
		return false
	}
	if _, err := os.Stat(app); err != nil {
		return true
	}
	return IsApp(app)
}

func (e *AgentEnv) mcpState(cmd string) string {
	switch {
	case cmd == "":
		return LinkNone
	case cmd == e.Command:
		return LinkCurrent
	case e.ourBinary(cmd):
		return LinkOurs
	}
	return LinkOther
}

// skillState classifies the agent's skill entry: a link to this app's skill
// is current; a link into another FennecCUA app (or one that is gone), or a
// copy of this skill `skill install` made, is ours.
func (e *AgentEnv) skillState(target string) string {
	fi, err := os.Lstat(target)
	if err != nil {
		return LinkNone
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		dest, err := os.Readlink(target)
		if err != nil {
			return LinkOther
		}
		if dest == e.SkillSrc {
			return LinkCurrent
		}
		if strings.Contains(dest, ".app/Contents/Resources/skills/"+SkillName) {
			if _, err := os.Stat(dest); err != nil {
				return LinkOurs
			}
			app := dest[:strings.Index(dest, ".app/")+len(".app")]
			if IsApp(app) {
				return LinkOurs
			}
		}
		return LinkOther
	}
	if fi.IsDir() {
		data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
		if err == nil && strings.Contains(string(data), "\nname: "+SkillName+"\n") {
			return LinkOurs
		}
	}
	return LinkOther
}

// Status reports one agent.
func (e *AgentEnv) Status(a Agent) AgentStatus {
	s := AgentStatus{ID: a.ID, Name: a.Name, MCP: LinkNone, Skill: LinkNone}
	if e.Find != nil {
		s.CLI = e.Find(a.CLI)
	}
	// Fennec puts launchers in front of the agents' commands that hand
	// them to Fennec, which may run them in the cloud: `claude mcp add`
	// through one configures the cloud agent. Use the local command the
	// launcher wraps.
	if s.CLI != "" {
		if orig, ok := FennecLauncherTarget(s.CLI); ok {
			s.CLI = orig
			if orig == "" {
				s.Error = "Fennec routes " + a.CLI + " elsewhere and the local " + a.Name + " it wraps is gone"
			}
		}
	}
	s.Installed = s.CLI != ""
	cmd, err := e.mcpCommand(a.ID)
	if err != nil {
		s.Error = err.Error()
	}
	s.MCP = e.mcpState(cmd)
	s.Skill = e.skillState(e.SkillTarget(a.ID))
	s.Connected = s.MCP == LinkCurrent && s.Skill == LinkCurrent
	s.Manual = e.manual(a)
	return s
}

// manual is the command that connects the agent by hand.
func (e *AgentEnv) manual(a Agent) string {
	q := shellQuote(e.Command)
	switch a.ID {
	case "claude":
		return "claude mcp add --scope user " + MCPName + " -- " + q + " mcp && " + q + " skill install --claude"
	case "codex":
		return "codex mcp add " + MCPName + " -- " + q + " mcp && " + q + " skill install --codex"
	}
	return ""
}

func addArgs(id, command string) []string {
	if id == "claude" {
		return []string{"mcp", "add", "--scope", "user", MCPName, "--", command, "mcp"}
	}
	return []string{"mcp", "add", MCPName, "--", command, "mcp"}
}

func removeArgs(id string) []string {
	if id == "claude" {
		return []string{"mcp", "remove", "--scope", "user", MCPName}
	}
	return []string{"mcp", "remove", MCPName}
}

// AgentTimeout bounds each command connect runs.
var AgentTimeout = 30 * time.Second

func (e *AgentEnv) run(ctx context.Context, cli string, args []string) error {
	if e.Run == nil {
		return errors.New("cannot run agent commands")
	}
	rctx, cancel := context.WithTimeout(ctx, AgentTimeout)
	defer cancel()
	out, err := e.Run(rctx, cli, args...)
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if msg != "" {
			return fmt.Errorf("%s %s: %v: %s", filepath.Base(cli), strings.Join(args[:2], " "), err, msg)
		}
		return fmt.Errorf("%s %s: %v", filepath.Base(cli), strings.Join(args[:2], " "), err)
	}
	return nil
}

// Connect registers fennec-cua as the agent's MCP server and links the
// skill. It replaces only entries of ours; an MCP server or a skill of the
// same name that is someone else's stays, and the result says so.
func (e *AgentEnv) Connect(ctx context.Context, a Agent) AgentStatus {
	s := e.Status(a)
	if !s.Installed {
		s.Error = a.Name + " is not installed"
		return s
	}
	var problems []string
	// Codex refuses to run while CODEX_HOME does not exist (a fresh
	// install that never started): make the agent's directory first.
	if err := os.MkdirAll(e.configDir(a.ID), 0o755); err != nil {
		problems = append(problems, err.Error())
	}
	switch s.MCP {
	case LinkOther:
		problems = append(problems, "an MCP server named "+MCPName+" already exists in "+a.Name+" and is not this app's; remove it there first")
	case LinkOurs:
		if err := e.run(ctx, s.CLI, removeArgs(a.ID)); err != nil {
			problems = append(problems, err.Error())
			break
		}
		fallthrough
	case LinkNone:
		if err := e.run(ctx, s.CLI, addArgs(a.ID, e.Command)); err != nil {
			problems = append(problems, err.Error())
		}
	}
	target := e.SkillTarget(a.ID)
	switch s.Skill {
	case LinkOther:
		problems = append(problems, target+" holds another skill; the "+SkillName+" skill was not linked")
	case LinkOurs:
		if err := os.RemoveAll(target); err != nil {
			problems = append(problems, err.Error())
			break
		}
		fallthrough
	case LinkNone:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			problems = append(problems, err.Error())
		} else if err := os.Symlink(e.SkillSrc, target); err != nil {
			problems = append(problems, err.Error())
		}
	}
	out := e.Status(a)
	if len(problems) > 0 {
		out.Error = strings.Join(problems, "; ")
	}
	return out
}

// Disconnect removes the agent's MCP entry and skill when they are ours.
func (e *AgentEnv) Disconnect(ctx context.Context, a Agent) AgentStatus {
	s := e.Status(a)
	var problems []string
	if (s.MCP == LinkCurrent || s.MCP == LinkOurs) && s.Installed {
		if err := e.run(ctx, s.CLI, removeArgs(a.ID)); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if s.Skill == LinkCurrent || s.Skill == LinkOurs {
		if err := os.RemoveAll(e.SkillTarget(a.ID)); err != nil {
			problems = append(problems, err.Error())
		}
	}
	out := e.Status(a)
	if len(problems) > 0 {
		out.Error = strings.Join(problems, "; ")
	}
	return out
}

// AgentByID finds a known agent.
func AgentByID(id string) (Agent, bool) {
	for _, a := range Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// SearchPath is where agents' commands are looked for: the user's login
// shell PATH (an app opened from Finder does not inherit it) and the usual
// install directories, without duplicates.
func SearchPath(home, loginPath, envPath string) string {
	var dirs []string
	seen := map[string]bool{}
	add := func(list string) {
		for _, d := range filepath.SplitList(list) {
			if d != "" && !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	add(loginPath)
	for _, d := range []string{
		filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local"),
		"/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".volta", "bin"), filepath.Join(home, ".cargo", "bin"),
	} {
		add(d)
	}
	add(envPath)
	add("/usr/bin:/bin:/usr/sbin:/sbin")
	return strings.Join(dirs, string(filepath.ListSeparator))
}

// LoginShellPath is $PATH as the user's interactive login shell sets it,
// "" when it cannot be read in time.
func LoginShellPath(ctx context.Context) string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const mark = "__FENNEC_CUA_PATH__"
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '\n`+mark+`%s\n' "$PATH"`)
	cmd.Stdin = nil
	out, _ := cmd.Output()
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, mark); ok {
			return p
		}
	}
	return ""
}

// FennecLauncherTarget reads a Fennec managed launcher or entry (a small
// shell script that hands an agent's command to Fennec): ok is false for
// any other file; target is the local command it wraps, "" when that is
// gone. When the wrapped file is one of an updater's versions (Claude
// Code's ~/.local/share/claude/versions/V) and no longer exists, the
// newest version beside it stands in.
func FennecLauncherTarget(path string) (target string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, _ := f.Read(head)
	text := string(head[:n])
	if !strings.HasPrefix(text, "#!") || !strings.Contains(text, "# Fennec managed ") {
		return "", false
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, "if [ -x '")
		if !found {
			continue
		}
		orig, _, found := strings.Cut(rest, "'")
		if !found || orig == "" {
			continue
		}
		if executable(orig) {
			return orig, true
		}
		if dir := filepath.Dir(orig); filepath.Base(dir) == "versions" {
			if newest := newestExecutable(dir); newest != "" {
				return newest, true
			}
		}
		return "", true
	}
	return "", true
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func newestExecutable(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	var bestTime time.Time
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		if best == "" || fi.ModTime().After(bestTime) {
			best, bestTime = p, fi.ModTime()
		}
	}
	return best
}

// FindIn looks a command up in a PATH list.
func FindIn(path, name string) string {
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
