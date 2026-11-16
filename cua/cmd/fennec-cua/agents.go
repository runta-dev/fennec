package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fennec/cua/internal/standalone"
)

// agentReport is `fennec-cua agents --json` and `connect --json`.
type agentReport struct {
	App     string                   `json:"app"`
	Mounted bool                     `json:"mounted"`
	Agents  []standalone.AgentStatus `json:"agents"`
}

// agentEnv is the environment connect works in for the app this fennec-cua
// runs from. With link set it first makes sure ~/.local/bin/fennec-cua leads
// to the app, so the agents start the command that follows the app.
func agentEnv(ctx context.Context, self string, link bool) (*standalone.AgentEnv, agentReport, error) {
	app := currentApp(self)
	if app == "" {
		return nil, agentReport{}, fmt.Errorf("this fennec-cua (%s) is not inside %s, which carries the skill", self, standalone.AppName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, agentReport{}, err
	}
	rep := agentReport{App: app, Mounted: standalone.Mounted(app)}
	exe := standalone.Executable(app)
	command := exe
	lnk := standalone.DefaultLayout(home).Link()
	if link && !rep.Mounted {
		_, _ = standalone.EnsureLink(lnk, exe)
	}
	if r, err := filepath.EvalSymlinks(lnk); err == nil {
		if want, err := filepath.EvalSymlinks(exe); err == nil && r == want {
			command = lnk
		}
	}
	search := standalone.SearchPath(home, standalone.LoginShellPath(ctx), os.Getenv("PATH"))
	env := &standalone.AgentEnv{
		Home:     home,
		Command:  command,
		SkillSrc: standalone.SkillDir(app),
		Find:     func(name string) string { return standalone.FindIn(search, name) },
		Run: func(ctx context.Context, path string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, path, args...)
			// The agents' commands are often scripts (node): give them
			// the user's PATH, which an app opened from Finder lacks.
			// FENNEC_LAUNCH_MODE would send it to Fennec again.
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "FENNEC_LAUNCH_MODE=") && !strings.HasPrefix(kv, "PATH=") {
					env = append(env, kv)
				}
			}
			cmd.Env = append(env, "PATH="+search)
			out, err := cmd.CombinedOutput()
			return string(out), err
		},
	}
	return env, rep, nil
}

// pickAgents reads agent ids; none means every known agent.
func pickAgents(args []string) ([]standalone.Agent, bool, error) {
	asJSON := false
	var out []standalone.Agent
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
			continue
		}
		ag, ok := standalone.AgentByID(strings.TrimPrefix(a, "--"))
		if !ok {
			return nil, false, fmt.Errorf("unknown agent %q (claude or codex)", a)
		}
		out = append(out, ag)
	}
	if len(out) == 0 {
		out = standalone.Agents
	}
	return out, asJSON, nil
}

func writeAgents(out io.Writer, rep agentReport, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(rep)
	}
	for _, s := range rep.Agents {
		state := "not installed"
		switch {
		case s.Connected:
			state = "connected"
		case s.Installed:
			state = "not connected (mcp " + s.MCP + ", skill " + s.Skill + ")"
		}
		fmt.Fprintf(out, "%-12s %s\n", s.Name, state)
		if s.Error != "" {
			fmt.Fprintf(out, "%-12s %s\n", "", s.Error)
		}
	}
	return nil
}

// agentsCmd is `fennec-cua agents [--json]`: which agents are installed and
// connected.
func agentsCmd(ctx context.Context, self string, args []string, out io.Writer) error {
	list, asJSON, err := pickAgents(args)
	if err != nil {
		return err
	}
	env, rep, err := agentEnv(ctx, self, false)
	if err != nil {
		return err
	}
	for _, a := range list {
		rep.Agents = append(rep.Agents, env.Status(a))
	}
	return writeAgents(out, rep, asJSON)
}

// connectCmd is `fennec-cua connect [claude] [codex] [--json]`: registers the
// MCP server in each installed agent and links the skill. Without names it
// connects every installed agent.
func connectCmd(ctx context.Context, self string, args []string, out io.Writer) error {
	list, asJSON, err := pickAgents(args)
	if err != nil {
		return err
	}
	env, rep, err := agentEnv(ctx, self, true)
	if err != nil {
		return err
	}
	if rep.Mounted {
		return errors.New("FennecCUA runs from the disk image; drag it to Applications, open it there, then connect")
	}
	failed := false
	for _, a := range list {
		s := env.Status(a)
		if s.Installed && !s.Connected {
			s = env.Connect(ctx, a)
		}
		failed = failed || s.Error != ""
		rep.Agents = append(rep.Agents, s)
	}
	if err := writeAgents(out, rep, asJSON); err != nil {
		return err
	}
	if failed && !asJSON {
		return errSilent
	}
	return nil
}

// disconnectCmd is `fennec-cua disconnect [claude] [codex] [--json]`.
func disconnectCmd(ctx context.Context, self string, args []string, out io.Writer) error {
	list, asJSON, err := pickAgents(args)
	if err != nil {
		return err
	}
	env, rep, err := agentEnv(ctx, self, false)
	if err != nil {
		return err
	}
	for _, a := range list {
		rep.Agents = append(rep.Agents, env.Disconnect(ctx, a))
	}
	return writeAgents(out, rep, asJSON)
}
