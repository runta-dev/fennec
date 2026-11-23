package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/board"
	"fennec/internal/queuescheduler"
	"fennec/internal/runta"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func startQueueReplication(ctx context.Context, s *daemonServices) {
	store := board.Store{Home: s.home, Role: "local"}
	if err := store.SetRouting(queueRouting(s, store)); err != nil {
		log.Printf("queue routing unavailable: %v", err)
		return
	}
	core := &boardLocalCore{lifetime: ctx}
	exe, _ := os.Executable()
	executor := queueMacExecutor{core: core, home: s.home, exe: exe}
	scheduler := queuescheduler.Scheduler{Store: store, Executor: executor, Command: shellArgument(exe) + " queue", OnError: func(err error) { log.Printf("queue execution: %v", err) }}
	// Restart begins fenced until a fresh replication handshake, preserving runs.
	if err := store.SetPrimary(false); err != nil {
		log.Printf("queue state unavailable: %v", err)
		return
	}
	go scheduler.Run(ctx)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		var peerSeen map[string]uint64
		var epoch uint64
		var identity string
		last := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			s.mu.Lock()
			ready := s.channel != nil && s.brokerReady
			s.mu.Unlock()
			if err := store.SetRouting(queueRouting(s, store)); err != nil {
				continue
			}
			lifecycle, token, current, authorized := s.account.Session()
			if !ready || !authorized {
				if last.IsZero() || time.Since(last) > 10*time.Second {
					_ = store.SetPrimary(false)
				}
				continue
			}
			step, cancel := context.WithTimeout(lifecycle, 8*time.Second)
			if current != epoch || identity == "" {
				api := runta.Client{Endpoint: s.account.Endpoint(), Token: token}
				owner, err := api.CurrentUser(step)
				if err != nil {
					cancel()
					continue
				}
				sum := sha256.Sum256([]byte(api.Endpoint + "/" + owner))
				identity = hex.EncodeToString(sum[:])
				epoch = current
				peerSeen = nil
			}
			pair := filepath.Join(s.home, ".fen/board/paired-account")
			if old, err := os.ReadFile(pair); err == nil && string(old) != identity {
				cancel()
				continue
			} else if os.IsNotExist(err) {
				if os.WriteFile(pair, []byte(identity), 0600) != nil {
					cancel()
					continue
				}
			}
			outbound, err := store.Sync(board.Exchange{Seen: peerSeen})
			caughtUp := !outbound.More
			if err == nil {
				raw, e := (boardClient{s: s}).call(step, "POST", "/v1/sync", outbound)
				err = e
				if err == nil {
					var incoming board.Exchange
					if json.Unmarshal(raw, &incoming) != nil {
						err = fmt.Errorf("invalid queue sync reply")
					} else {
						caughtUp = caughtUp && !incoming.More
						_, err = store.Sync(incoming)
						if err == nil {
							peerSeen = incoming.Seen
						}
					}
				}
			}
			if err == nil {
				last = time.Now()
				_ = store.RecordSync(peerSeen)
				if caughtUp {
					err = store.SetPrimary(true)
				}
			} else if last.IsZero() || time.Since(last) > 10*time.Second {
				_ = store.SetPrimary(false)
			}
			cancel()
		}
	}()
}

func queueRouting(s *daemonServices, store board.Store) board.Routing {
	result := store.Routing()
	s.mu.Lock()
	known := s.config.RuntimeID != ""
	device := s.config.Device
	selected := map[string]bool{}
	for agent, v := range s.selected {
		selected[agent] = v
	}
	s.mu.Unlock()
	if !known {
		var saved struct {
			Agents []string `json:"agents"`
		}
		if raw, err := os.ReadFile(runta.TakeoverPreferencesPath(s.home)); err == nil && json.Unmarshal(raw, &saved) == nil {
			known = true
			for _, agent := range saved.Agents {
				selected[agent] = true
			}
		}
	}
	if known {
		for agent := range board.Agents {
			result.Locations[agent] = "local"
			if selected[agent] {
				result.Locations[agent] = "cloud"
			}
		}
	}
	if device != "" {
		result.Device = device
	}
	return result
}
func shellArgument(value string) string {
	out := "'"
	for _, r := range value {
		if r == '\'' {
			out += "'\\''"
		} else {
			out += string(r)
		}
	}
	return out + "'"
}

type queueMacExecutor struct {
	core      *boardLocalCore
	home, exe string
}

func (e queueMacExecutor) List(ctx context.Context) ([]queuescheduler.Session, error) {
	raw, err := e.core.call(ctx, "terminal.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var list struct {
		Terminals []struct {
			State  string            `json:"state"`
			Labels map[string]string `json:"labels"`
		} `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, fmt.Errorf("invalid local terminal listing")
	}
	out := []queuescheduler.Session{}
	for _, t := range list.Terminals {
		out = append(out, queuescheduler.Session{ID: t.Labels["fennec.session"], State: t.State})
	}
	return out, nil
}
func (e queueMacExecutor) Start(ctx context.Context, p board.Project, r board.Request, prompt string) error {
	// The persisted marker distinguishes a never-sent run from an uncertain
	// terminal.run. An uncertain request is inspected, never sent a second time.
	root := filepath.Join(e.home, ".fen/board/launches")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile(filepath.Join(root, r.Session+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("local launch needs reconciliation: %w", err)
	}
	raw, _ := json.Marshal(map[string]string{"run": r.Session, "queue": p.ID})
	_, err = marker.Write(raw)
	if err == nil {
		err = marker.Sync()
	}
	marker.Close()
	if err != nil {
		return err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	argv := []string{shell, "-lc", `exec "$@"`, "fennec-queue", e.exe, "local-agent", r.Agent}
	worker := board.Worker{Agent: r.Agent}
	for _, w := range p.Workers {
		if w.Agent == r.Agent {
			worker = w
			break
		}
	}
	argv = append(argv, launchArguments(worker, prompt)...)
	_, err = e.core.call(ctx, "terminal.run", map[string]any{"argv": argv, "cwd": e.home, "env": map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, "labels": map[string]string{"fennec.session": r.Session, "fennec.agent": r.Agent, "fennec.queue": p.ID, "fennec.request": fmt.Sprint(r.Number), "fennec.run": r.Session}, "size": map[string]int{"cols": 120, "rows": 40}})
	return err
}
