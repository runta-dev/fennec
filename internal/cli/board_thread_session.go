package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func (e queueMacExecutor) threadSession(ctx context.Context, project string, number int) (r.LocalTerminal, error) {
	terminal, _, err := e.continueThread(ctx, project, number)
	return terminal, err
}

// continueThread also reports whether the terminal carries the agent's earlier
// conversation for this thread (see runtime.ContinueThread).
func (e queueMacExecutor) continueThread(ctx context.Context, project string, number int) (terminal r.LocalTerminal, continued bool, err error) {
	store := e.board()
	root := filepath.Join(store.Root(), "launches")
	if err := os.MkdirAll(root, 0700); err != nil {
		return r.LocalTerminal{}, false, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d", project, number))))
	lock, err := os.OpenFile(filepath.Join(root, key+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return r.LocalTerminal{}, false, err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return r.LocalTerminal{}, false, err
		}
		select {
		case <-ctx.Done():
			return r.LocalTerminal{}, false, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	request, err := store.Request(project, number)
	if err != nil {
		return r.LocalTerminal{}, false, err
	}
	if len(request.Runs) == 0 {
		return r.LocalTerminal{}, false, fmt.Errorf("this thread has not started yet")
	}
	latest := request.Runs[len(request.Runs)-1]
	if latest.Location != "local" {
		return r.LocalTerminal{}, false, fmt.Errorf("this thread runs on the cloud computer")
	}
	agent := latest.Agent
	if agent == "" {
		agent = request.Agent
	}
	if agent == "" {
		for i := len(request.Messages) - 1; i >= 0; i-- {
			if request.Messages[i].Agent != "" {
				agent = request.Messages[i].Agent
				break
			}
		}
	}
	raw, err := e.core.call(ctx, "terminal.list", map[string]any{})
	if err != nil {
		return r.LocalTerminal{}, false, err
	}
	var list struct {
		Terminals []r.LocalTerminal `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return r.LocalTerminal{}, false, fmt.Errorf("invalid terminal listing")
	}
	previous, cwd := "", ""
	for i := len(request.Runs) - 1; i >= 0; i-- {
		run := request.Runs[i]
		if run.State == "not_started" || run.Location != "local" || run.Agent != "" && run.Agent != agent {
			continue
		}
		// Only a run that launched can be resumed: one whose launch failed
		// (FEN-11) left no session behind. Launches are recorded in root first.
		if previous == "" && (run.ID != request.Session || run.State != "starting" || run.Session != run.ID) && launchedLocally(root, run.Session) {
			previous = run.Session
		}
		for _, t := range list.Terminals {
			if t.Labels["fennec.session"] == run.Session && t.Labels["fennec.agent"] == agent {
				if cwd == "" {
					cwd = t.Labels["fennec.directory"]
				}
				if t.State == "live" {
					terminal = t
					break
				}
			}
		}
		if terminal.Address != "" {
			continued = run.Session != request.Session
			break
		}
	}
	if terminal.Address == "" {
		continued = previous != ""
		id := request.Session
		if latest.State != "starting" || id == "" || latest.Session != latest.ID {
			id = fmt.Sprintf("%x", sha256.Sum256([]byte(previous+"/resume")))[:32]
		}
		args := []string{}
		if previous != "" {
			if cwd == "" {
				return r.LocalTerminal{}, false, fmt.Errorf("the previous conversation's working directory is unavailable")
			}
			_, args, err = r.ResumeArguments(setup.LocalAgentHome(ctx, agent), agent, cwd, id, "")
			if errors.Is(err, r.ErrResumeUnavailable) {
				// The earlier run never got a conversation (FEN-11): start
				// fresh with the full thread instead of failing for good.
				previous, continued, args, err = "", false, []string{}, nil
			} else if err != nil {
				return r.LocalTerminal{}, false, err
			}
		}
		if previous == "" {
			cwd = filepath.Join(e.home, ".fen/queue-sessions", id)
			if err = os.MkdirAll(cwd, 0700); err != nil {
				return r.LocalTerminal{}, false, err
			}
		}
		if agent == "claude" {
			// Nobody is there to answer Claude Code's "Quick safety check"
			// folder trust prompt in a queue session; the launch would sit
			// on it and the scheduler would never see the agent start.
			if err = setup.TrustClaudeFolders(setup.ClaudeProfile(e.home, setup.LocalAgentHome(ctx, agent)), cwd); err != nil {
				return r.LocalTerminal{}, false, fmt.Errorf("trust Claude working directory: %w", err)
			}
		}
		marker, err := os.OpenFile(filepath.Join(root, id+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return r.LocalTerminal{}, false, fmt.Errorf("local launch needs reconciliation: %w", err)
		}
		_, err = marker.WriteString("pending")
		if err == nil {
			err = marker.Sync()
		}
		marker.Close()
		if err != nil {
			return r.LocalTerminal{}, false, err
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		argv := []string{shell, "-lc", `exec "$@"`, "fennec-queue", e.exe, "local-agent", agent}
		p, err := store.Project(project)
		if err != nil {
			return r.LocalTerminal{}, false, err
		}
		for _, w := range p.Workers {
			if w.Agent == agent && w.Model != "" && (agent == "claude" || agent == "codex") {
				args = append(args, "--model", w.Model)
				break
			}
		}
		argv = append(argv, args...)
		labels := map[string]string{"fennec.session": id, "fennec.agent": agent, "fennec.queue": project, "fennec.request": fmt.Sprint(number), "fennec.directory": cwd}
		labels["fennec.board"] = store.Namespace
		run := map[string]any{"argv": argv, "cwd": cwd, "env": map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, "labels": labels, "size": map[string]int{"cols": 120, "rows": 40}}
		// Nobody is attached yet; answer the agent's color queries with the
		// last viewer's theme instead of herdr-core's dark default (FEN-33).
		if display := terminalstream.LastDisplay(e.home); display != nil {
			run["display"] = display
		}
		raw, err = e.core.call(ctx, "terminal.run", run)
		if err != nil {
			return r.LocalTerminal{}, false, err
		}
		if json.Unmarshal(raw, &terminal) != nil || terminal.Address == "" {
			return r.LocalTerminal{}, false, fmt.Errorf("terminal launch was not confirmed")
		}
		terminal.Labels = labels
	}
	if err = store.BindSession(project, number, request.Session, terminal.Labels["fennec.session"]); err != nil {
		return r.LocalTerminal{}, false, err
	}
	return terminal, continued, nil
}

// launchedLocally reports whether continueThread ever launched this session.
func launchedLocally(root, session string) bool {
	if session == "" || strings.ContainsAny(session, "/\\.") {
		return false
	}
	_, err := os.Stat(filepath.Join(root, session+".json"))
	return err == nil
}
