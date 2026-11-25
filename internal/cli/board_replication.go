package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	"fennec/internal/queuescheduler"
	"fennec/internal/runta"
	"fennec/internal/terminalstream"
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
	s.mu.Lock()
	s.boardCore = core
	s.mu.Unlock()
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
		// Log only transitions: a silent 3.5-hour outage (THI-55) left the
		// Mac showing a frozen board with nothing in daemon.log to explain it.
		health := syncHealth{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			s.mu.Lock()
			ready := s.channel != nil && s.brokerReady
			channel := s.channel
			s.mu.Unlock()
			if err := store.SetRouting(queueRouting(s, store)); err != nil {
				continue
			}
			lifecycle, principal, current, authorized := s.account.Session()
			if !ready || !authorized {
				health.failed(fmt.Sprintf("channel ready=%t authorized=%t", ready, authorized))
				if last.IsZero() || time.Since(last) > 10*time.Second {
					_ = store.SetPrimary(false)
				}
				continue
			}
			step, cancel := context.WithTimeout(lifecycle, 8*time.Second)
			// Old peers would drop image fields and start an incomplete request.
			// Wait for the matching cloud service before publishing any new records.
			transport, transportErr := channel.get(step)
			if transportErr == nil {
				transportErr = transport.Ready(step)
			}
			if transportErr != nil || !transport.Supports(terminalstream.CapabilityBoardImages) {
				if transportErr != nil {
					health.failed("transport: " + transportErr.Error())
				} else {
					health.failed("cloud service lacks board image support")
				}
				if last.IsZero() || time.Since(last) > 10*time.Second {
					_ = store.SetPrimary(false)
				}
				cancel()
				continue
			}
			if current != epoch || identity == "" {
				// The board is shared by the devices of one owner: a Runta account,
				// or a paired self-hosted computer.
				scope := "byoc/" + principal.Machine
				if token, isRunta := principal.RuntaToken(); isRunta {
					api := runta.Client{Endpoint: s.account.Endpoint(), Token: token}
					owner, err := api.CurrentUser(step)
					if err != nil {
						health.failed("account: " + err.Error())
						cancel()
						continue
					}
					scope = api.Endpoint + "/" + owner
				}
				sum := sha256.Sum256([]byte(scope))
				identity = hex.EncodeToString(sum[:])
				epoch = current
				peerSeen = nil
			}
			pair := filepath.Join(s.home, ".fen/board/paired-account")
			if old, err := os.ReadFile(pair); err == nil && string(old) != identity {
				health.failed("board is paired with a different account")
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
			if peerSeen == nil {
				// Without the cloud's cursor every event qualifies, so the first
				// exchange after a daemon restart would resend the whole journal.
				// On a slow channel that never fits the step timeout and each
				// retry starts over, wedging the queue. Learn the cursor first.
				outbound.Events, outbound.More = nil, false
				caughtUp = false
			}
			if err == nil {
				raw, e := (boardClient{s: s}).call(step, "POST", "/v1/sync", outbound)
				err = e
				if err == nil {
					var incoming board.Exchange
					if json.Unmarshal(raw, &incoming) != nil {
						err = fmt.Errorf("invalid queue sync reply")
					} else {
						caughtUp = caughtUp && !incoming.More
						var local board.Exchange
						local, err = store.Sync(incoming)
						if err == nil {
							peerSeen = incoming.Seen
							// Metadata may arrive before its blobs. Executors leave
							// such requests queued until every original is present.
							last = time.Now()
							_ = store.RecordImageSync(peerSeen, len(incoming.Missing) > 0 || len(local.Missing) > 0)
							err = syncQueueAttachments(step, boardClient{s: s}, store, incoming.Missing, local.Missing)
						}
					}
				}
			}
			if err == nil {
				health.ok()
				last = time.Now()
				if caughtUp {
					err = store.SetPrimary(true)
				}
			} else {
				reason := "sync: " + err.Error()
				if cause := errors.Unwrap(err); cause != nil {
					reason += " (" + cause.Error() + ")"
				}
				health.failed(reason)
				if last.IsZero() || time.Since(last) > 10*time.Second {
					_ = store.SetPrimary(false)
				}
			}
			cancel()
		}
	}()
}

// syncHealth logs queue sync failures and recovery once per change.
type syncHealth struct {
	reason string
	since  time.Time
}

func (h *syncHealth) failed(reason string) {
	if reason == h.reason {
		return
	}
	if h.reason == "" {
		h.since = time.Now()
	}
	h.reason = reason
	log.Printf("queue sync failing: %s", reason)
}

func (h *syncHealth) ok() {
	if h.reason != "" {
		log.Printf("queue sync recovered after %s", time.Since(h.since).Round(time.Second))
	}
	h.reason = ""
}

func syncQueueAttachments(ctx context.Context, remote boardClient, local board.Store, push, pull []board.MissingAttachment) error {
	for _, missing := range push {
		chunk, err := local.ReadAttachmentChunk(missing.Attachment.ID, missing.Offset)
		if err != nil {
			continue
		}
		if _, err = remote.call(ctx, "POST", "/v1/attachments/"+chunk.Attachment.ID, chunk); err != nil {
			return err
		}
	}
	for _, missing := range pull {
		raw, err := remote.call(ctx, "GET", fmt.Sprintf("/v1/attachments/%s/%d", missing.Attachment.ID, missing.Offset), nil)
		if err != nil {
			return err
		}
		var chunk board.AttachmentChunk
		if json.Unmarshal(raw, &chunk) != nil {
			return fmt.Errorf("invalid attachment chunk")
		}
		if chunk.Attachment.ID != missing.Attachment.ID || chunk.Offset != missing.Offset {
			return fmt.Errorf("attachment identity mismatch")
		}
		if err = local.WriteAttachmentChunk(chunk); err != nil {
			return err
		}
	}
	return nil
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
	raw, err := e.core.call(ctx, "terminal.list", queuescheduler.ListAll)
	if err != nil {
		return nil, err
	}
	var list struct {
		Terminals []struct {
			Address  string            `json:"address"`
			State    string            `json:"state"`
			Title    string            `json:"title"`
			Labels   map[string]string `json:"labels"`
			Status   json.RawMessage   `json:"status"`
			StatusMS int64             `json:"status_ms"`
		} `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, fmt.Errorf("invalid local terminal listing")
	}
	out := []queuescheduler.Session{}
	for _, t := range list.Terminals {
		out = append(out, queuescheduler.Session{ID: t.Labels["fennec.session"], Address: t.Address, State: t.State, Title: t.Title, Status: queuescheduler.AgentStatus(t.Status), App: queuescheduler.AgentApp(t.Status), StatusAt: queuescheduler.StatusTime(t.StatusMS), Background: queuescheduler.BackgroundOnly(t.Status)})
	}
	return out, nil
}
func (e queueMacExecutor) End(ctx context.Context, address string) error {
	_, err := e.core.call(ctx, "terminal.kill", map[string]any{"terminal": address})
	return err
}
func (e queueMacExecutor) Start(ctx context.Context, p board.Project, request board.Request, prompt string) error {
	terminal, err := e.threadSession(ctx, p.ID, request.Number)
	if err != nil {
		return err
	}
	return queuescheduler.SendRequest(ctx, board.Store{Home: e.home, Role: "local"}, e.core.call, terminal.Address, request, prompt)
}
