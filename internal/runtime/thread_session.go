package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fennec/internal/board"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ThreadSession is shared by the scheduler and explicit navigation. Opening a
// thread's terminal never submits work; only the scheduler delivers its input.
func (runtime *LocalRuntime) ThreadSession(ctx context.Context, store board.Store, project string, number int) (LocalTerminal, error) {
	root := filepath.Join(runtime.Home, ".fen/board/session-locks")
	if err := os.MkdirAll(root, 0700); err != nil {
		return LocalTerminal{}, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d", project, number))))
	lock, err := os.OpenFile(filepath.Join(root, key), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return LocalTerminal{}, err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return LocalTerminal{}, err
		}
		select {
		case <-ctx.Done():
			return LocalTerminal{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	request, err := store.Request(project, number)
	if err != nil {
		return LocalTerminal{}, err
	}
	if len(request.Runs) == 0 {
		return LocalTerminal{}, fmt.Errorf("this thread has not started yet")
	}
	latest := request.Runs[len(request.Runs)-1]
	if latest.Location != "cloud" {
		return LocalTerminal{}, fmt.Errorf("this thread runs on the Mac")
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
		return LocalTerminal{}, err
	}
	var list struct {
		Terminals []LocalTerminal `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return LocalTerminal{}, fmt.Errorf("invalid session listing")
	}
	var terminal LocalTerminal
	previous := ""
	for i := len(request.Runs) - 1; i >= 0; i-- {
		run := request.Runs[i]
		if run.State == "not_started" || run.Location != "cloud" || run.Agent != "" && run.Agent != agent {
			continue
		}
		if previous == "" && (run.ID != request.Session || run.State != "starting" || run.Session != run.ID) {
			previous = run.Session
		}
		for _, t := range list.Terminals {
			if t.Labels["fennec.session"] == run.Session && t.State == "live" && t.Labels["fennec.agent"] == agent {
				terminal = t
				break
			}
		}
		if terminal.Address != "" {
			break
		}
	}
	if terminal.Address == "" {
		p, err := store.Project(project)
		if err != nil {
			return LocalTerminal{}, err
		}
		args := []string{}
		for _, worker := range p.Workers {
			if worker.Agent == agent && worker.Model != "" && (agent == "claude" || agent == "codex") {
				args = append(args, "--model", worker.Model)
				break
			}
		}
		id := request.Session
		if latest.State != "starting" || id == "" || latest.Session != latest.ID {
			id = fmt.Sprintf("%x", sha256.Sum256([]byte(previous+"/resume")))[:32]
		}
		terminal, err = runtime.Launch(ctx, LocalLaunch{ID: id, ResumeFrom: previous, Agent: agent, Device: store.Routing().Device, LocalDirectory: "/", Arguments: args})
		if err != nil {
			return LocalTerminal{}, err
		}
		if terminal.State == "finished" {
			return LocalTerminal{}, fmt.Errorf("the resumed session has ended")
		}
		if terminal.Labels == nil {
			terminal.Labels = map[string]string{}
		}
		terminal.Labels["fennec.session"] = id
	}
	id := terminal.Labels["fennec.session"]
	_, err = runtime.Query(ctx, "terminal.label", map[string]any{"terminal": terminal.Address, "labels": map[string]string{"fennec.queue": project, "fennec.request": fmt.Sprint(number)}})
	if err != nil {
		return LocalTerminal{}, err
	}
	if err = store.BindSession(project, number, request.Session, id); err != nil {
		return LocalTerminal{}, err
	}
	terminal.Labels["fennec.queue"] = project
	terminal.Labels["fennec.request"] = fmt.Sprint(number)
	return terminal, nil
}
