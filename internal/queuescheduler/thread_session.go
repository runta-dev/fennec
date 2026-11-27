package queuescheduler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	r "fennec/internal/runtime"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ThreadSession finds or starts the cloud session of a queue thread. The queue
// owns the mapping from threads to sessions; runtime only runs them (FEN-81).
// It is shared by the scheduler and explicit navigation. Opening a
// thread's terminal never submits work; only the scheduler delivers its input.
func ThreadSession(ctx context.Context, runtime *r.LocalRuntime, store board.Store, project string, number int) (r.LocalTerminal, error) {
	terminal, _, err := ContinueThread(ctx, runtime, store, project, number)
	return terminal, err
}

// ContinueThread is ThreadSession that also reports whether the terminal
// carries the agent's earlier conversation for this thread: a live terminal of
// an earlier run, or a launch that resumed one. A fresh launch does not.
func ContinueThread(ctx context.Context, runtime *r.LocalRuntime, store board.Store, project string, number int) (terminal r.LocalTerminal, continued bool, err error) {
	root := filepath.Join(runtime.Home, ".fen/board/session-locks")
	if err := os.MkdirAll(root, 0700); err != nil {
		return r.LocalTerminal{}, false, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d", project, number))))
	lock, err := os.OpenFile(filepath.Join(root, key), os.O_CREATE|os.O_RDWR, 0600)
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
	if latest.Location != "cloud" {
		return r.LocalTerminal{}, false, fmt.Errorf("this thread runs on the Mac")
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
	raw, err := runtime.Query(ctx, "terminal.list", map[string]any{})
	if err != nil {
		return r.LocalTerminal{}, false, err
	}
	var list struct {
		Terminals []r.LocalTerminal `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return r.LocalTerminal{}, false, fmt.Errorf("invalid session listing")
	}
	previous := ""
	for i := len(request.Runs) - 1; i >= 0; i-- {
		run := request.Runs[i]
		if run.State == "not_started" || run.Location != "cloud" || run.Agent != "" && run.Agent != agent {
			continue
		}
		// Only a run that launched can be resumed: one whose launch failed
		// (FEN-11: a follow-up whose resume failed) left no session behind.
		if previous == "" && (run.ID != request.Session || run.State != "starting" || run.Session != run.ID) && runtime.Launched(run.Session) {
			previous = run.Session
		}
		for _, t := range list.Terminals {
			if t.Labels["fennec.session"] == run.Session && t.State == "live" && t.Labels["fennec.agent"] == agent {
				terminal = t
				break
			}
		}
		if terminal.Address != "" {
			continued = run.Session != request.Session
			break
		}
	}
	if terminal.Address == "" {
		p, err := store.Project(project)
		if err != nil {
			return r.LocalTerminal{}, false, err
		}
		args := LaunchArguments(p, request, agent)
		id := request.Session
		if latest.State != "starting" || id == "" || latest.Session != latest.ID {
			id = fmt.Sprintf("%x", sha256.Sum256([]byte(previous+"/resume")))[:32]
		}
		continued = previous != ""
		launch := r.LocalLaunch{ID: id, ResumeFrom: previous, Agent: agent, Device: store.Routing().Device, LocalDirectory: "/", Arguments: args}
		terminal, err = runtime.Launch(ctx, launch)
		if errors.Is(err, r.ErrResumeUnavailable) {
			// The earlier run never got a conversation (FEN-11: its input was
			// refused). Nothing launched yet, so start fresh with the full
			// thread instead of failing the follow-up for good.
			launch.ResumeFrom, continued = "", false
			terminal, err = runtime.Launch(ctx, launch)
		}
		if err != nil {
			return r.LocalTerminal{}, false, err
		}
		if terminal.State == "finished" {
			return r.LocalTerminal{}, false, fmt.Errorf("the resumed session has ended")
		}
		if terminal.Labels == nil {
			terminal.Labels = map[string]string{}
		}
		terminal.Labels["fennec.session"] = id
	}
	id := terminal.Labels["fennec.session"]
	_, err = runtime.Query(ctx, "terminal.label", map[string]any{"terminal": terminal.Address, "labels": map[string]string{"fennec.queue": project, "fennec.request": fmt.Sprint(number)}})
	if err != nil {
		return r.LocalTerminal{}, false, err
	}
	if err = store.BindSession(project, number, request.Session, id); err != nil {
		return r.LocalTerminal{}, false, err
	}
	terminal.Labels["fennec.queue"] = project
	terminal.Labels["fennec.request"] = fmt.Sprint(number)
	return terminal, continued, nil
}

// LaunchArguments are the model and reasoning-effort flags for the agent of a
// thread: what the person picked in the composer (FEN-145), else the model of
// the queue's worker entry. Only Claude Code and Codex take them.
func LaunchArguments(p board.Project, request board.Request, agent string) []string {
	if agent != "claude" && agent != "codex" {
		return nil
	}
	model, effort := "", ""
	if request.Agent == agent && (board.Choice{Agent: agent, Model: request.Model, Effort: request.Effort}).Valid() {
		model, effort = request.Model, request.Effort
	}
	if model == "" {
		for _, worker := range p.Workers {
			if worker.Agent == agent && worker.Model != "" {
				model = worker.Model
				break
			}
		}
	}
	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		if agent == "claude" {
			args = append(args, "--effort", effort)
		} else {
			args = append(args, "-c", "model_reasoning_effort="+effort)
		}
	}
	return args
}
