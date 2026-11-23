package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fennec/internal/board"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (e queueMacExecutor) threadSession(ctx context.Context, project string, number int) (r.LocalTerminal, error) {
	store := board.Store{Home: e.home, Role: "local"}
	root := filepath.Join(e.home, ".fen/board/launches")
	if err := os.MkdirAll(root, 0700); err != nil {
		return r.LocalTerminal{}, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d", project, number))))
	lock, err := os.OpenFile(filepath.Join(root, key+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return r.LocalTerminal{}, err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return r.LocalTerminal{}, err
		}
		select {
		case <-ctx.Done():
			return r.LocalTerminal{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	request, err := store.Request(project, number)
	if err != nil {
		return r.LocalTerminal{}, err
	}
	if len(request.Runs) == 0 {
		return r.LocalTerminal{}, fmt.Errorf("this thread has not started yet")
	}
	latest := request.Runs[len(request.Runs)-1]
	if latest.Location != "local" {
		return r.LocalTerminal{}, fmt.Errorf("this thread runs on the cloud computer")
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
		return r.LocalTerminal{}, err
	}
	var list struct {
		Terminals []r.LocalTerminal `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return r.LocalTerminal{}, fmt.Errorf("invalid terminal listing")
	}
	var terminal r.LocalTerminal
	previous, cwd := "", ""
	for i := len(request.Runs) - 1; i >= 0; i-- {
		run := request.Runs[i]
		if run.State == "not_started" || run.Location != "local" || run.Agent != "" && run.Agent != agent {
			continue
		}
		if previous == "" && (run.ID != request.Session || run.State != "starting" || run.Session != run.ID) {
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
			break
		}
	}
	if terminal.Address == "" {
		id := request.Session
		if latest.State != "starting" || id == "" || latest.Session != latest.ID {
			id = fmt.Sprintf("%x", sha256.Sum256([]byte(previous+"/resume")))[:32]
		}
		args := []string{}
		if previous != "" {
			if cwd == "" {
				return r.LocalTerminal{}, fmt.Errorf("the previous conversation's working directory is unavailable")
			}
			_, args, err = r.ResumeArguments(setup.LocalAgentHome(ctx, agent), agent, cwd, id, "")
			if err != nil {
				return r.LocalTerminal{}, err
			}
		} else {
			cwd = filepath.Join(e.home, ".fen/queue-sessions", id)
			if err = os.MkdirAll(cwd, 0700); err != nil {
				return r.LocalTerminal{}, err
			}
		}
		marker, err := os.OpenFile(filepath.Join(root, id+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return r.LocalTerminal{}, fmt.Errorf("local launch needs reconciliation: %w", err)
		}
		_, err = marker.WriteString("pending")
		if err == nil {
			err = marker.Sync()
		}
		marker.Close()
		if err != nil {
			return r.LocalTerminal{}, err
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		argv := []string{shell, "-lc", `exec "$@"`, "fennec-queue", e.exe, "local-agent", agent}
		p, err := store.Project(project)
		if err != nil {
			return r.LocalTerminal{}, err
		}
		for _, w := range p.Workers {
			if w.Agent == agent && w.Model != "" && (agent == "claude" || agent == "codex") {
				args = append(args, "--model", w.Model)
				break
			}
		}
		argv = append(argv, args...)
		labels := map[string]string{"fennec.session": id, "fennec.agent": agent, "fennec.queue": project, "fennec.request": fmt.Sprint(number), "fennec.directory": cwd}
		raw, err = e.core.call(ctx, "terminal.run", map[string]any{"argv": argv, "cwd": cwd, "env": map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, "labels": labels, "size": map[string]int{"cols": 120, "rows": 40}})
		if err != nil {
			return r.LocalTerminal{}, err
		}
		if json.Unmarshal(raw, &terminal) != nil || terminal.Address == "" {
			return r.LocalTerminal{}, fmt.Errorf("terminal launch was not confirmed")
		}
		terminal.Labels = labels
	}
	if err = store.BindSession(project, number, request.Session, terminal.Labels["fennec.session"]); err != nil {
		return r.LocalTerminal{}, err
	}
	return terminal, nil
}
