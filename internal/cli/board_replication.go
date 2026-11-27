package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	"fennec/internal/queuescheduler"
	"fennec/internal/runta"
	"fennec/internal/terminalstream"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"
)

func startQueueReplication(ctx context.Context, s *daemonServices) {
	store := s.localBoard()
	if err := store.SetRouting(queueRouting(s, store)); err != nil {
		log.Printf("queue routing unavailable: %v", err)
		return
	}
	core := &boardLocalCore{lifetime: ctx}
	s.mu.Lock()
	s.boardCore = core
	s.mu.Unlock()
	exe, _ := os.Executable()
	// Restart begins fenced until a fresh replication handshake, preserving runs.
	if err := store.SetPrimary(false); err != nil {
		log.Printf("queue state unavailable: %v", err)
		return
	}
	go func() {
		startScheduler := func(store board.Store) context.CancelFunc {
			life, cancel := s.boardLifetime(ctx)
			executor := queueMacExecutor{core: core, home: s.home, exe: exe, store: store}
			scheduler := queuescheduler.Scheduler{Store: store, Executor: executor, Command: shellArgument(exe) + " queue --scope " + store.Namespace, OnError: func(err error) { log.Printf("queue execution: %v", err) }}
			go scheduler.Run(life)
			return cancel
		}
		stopScheduler := startScheduler(store)
		defer func() { stopScheduler() }()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		var peerSeen map[string]uint64
		var epoch uint64
		starterChecked := false
		last := time.Time{}
		// Log only transitions: a silent 3.5-hour outage (THI-55) left the
		// Mac showing a frozen board with nothing in daemon.log to explain it.
		health := syncHealth{}
		// After a good exchange with a cloud that holds idle exchanges open
		// (CapabilityBoardWait), the next one starts at once: the Mac is
		// always waiting on the cloud, which answers the moment its board
		// changes. Everything else still paces itself on the ticker (FEN-193).
		again := false
		for {
			if again {
				again = false
				if ctx.Err() != nil {
					return
				}
			} else {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
			lifecycle, _, current, authorized := s.account.Session()
			next := s.localBoard()
			if next.Namespace != store.Namespace || current != epoch {
				stopScheduler()
				_ = store.SetPrimary(false)
				store = next
				_ = store.SetPrimary(false)
				peerSeen, starterChecked, last = nil, false, time.Time{}
				epoch = current
				stopScheduler = startScheduler(store)
			}
			s.mu.Lock()
			ready := s.channel != nil && s.brokerReady
			channel := s.channel
			s.mu.Unlock()
			if err := store.SetRouting(queueRouting(s, store)); err != nil {
				continue
			}
			if !ready || !authorized || store.Namespace == "local" {
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
			remote := boardClient{s: s, scope: store.Namespace, channel: channel}
			canWait := transport.Supports(terminalstream.CapabilityBoardWait)
			localStamp := store.ChangeStamp()
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
			// Do not publish a format an older executor cannot read. It will
			// resume from the same cursor once its advertised capability updates.
			if err == nil && !transport.Supports(terminalstream.CapabilityBoardWebP) {
				for _, event := range outbound.Events {
					var messages []board.Message
					if json.Unmarshal(event.Fields["messages"], &messages) != nil {
						continue
					}
					for _, message := range messages {
						for _, image := range message.Attachments {
							if image.MediaType == "image/webp" {
								err = fmt.Errorf("cloud service needs WebP image support")
							}
						}
					}
				}
			}
			// Nothing to send and nothing pending: let the cloud hold the
			// exchange until its board changes. A write on this Mac meanwhile
			// (a reply, a new request) cancels the wait so it goes out now.
			call, interrupted := step, atomic.Bool{}
			stopWatch := func() {}
			if err == nil && canWait && peerSeen != nil && caughtUp && len(outbound.Events) == 0 {
				outbound.Wait = int(syncWait / time.Millisecond)
				call, stopWatch = context.WithCancel(step)
				go func() {
					if store.WaitChange(call, localStamp, syncWait+time.Second) != localStamp {
						interrupted.Store(true)
						stopWatch()
					}
				}()
			}
			if err == nil {
				raw, e := remote.call(call, "POST", "/v1/sync", outbound)
				stopWatch()
				if e != nil && interrupted.Load() {
					cancel()
					again = true
					continue
				}
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
							// Metadata's short deadline must not repeatedly cancel a
							// larger image at offset zero on a slow resident link.
							images, stopImages := context.WithTimeout(lifecycle, 30*time.Second)
							if transport.Supports(terminalstream.CapabilityBoardWebP) || !hasWebP(incoming.Missing) {
								err = syncQueueAttachments(images, remote, store, incoming.Missing, local.Missing)
							} else {
								err = fmt.Errorf("cloud service needs WebP image support")
							}
							stopImages()
						}
					}
				}
			}
			if err == nil {
				health.ok()
				again = canWait
				_ = store.SetSyncBlocked("")
				last = time.Now()
				if caughtUp {
					err = store.SetPrimary(true)
				}
				// Only once the Mac holds the whole board can it tell a new
				// board from one that has not arrived yet.
				if caughtUp && !starterChecked {
					starterChecked = true
					name, description := starterQueueCopy(s.home)
					if added, e := store.AddStarterProject(name, description); e != nil {
						log.Printf("starter queue: %v", e)
					} else if added {
						log.Printf("starter queue added to a new board")
					}
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

// syncWait is how long an idle exchange waits on the cloud for a change. It
// stays well inside the 8 s step so a slow link still gets its answer.
const syncWait = 4 * time.Second

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

func hasWebP(missing []board.MissingAttachment) bool {
	for _, m := range missing {
		if m.Attachment.MediaType == "image/webp" {
			return true
		}
	}
	return false
}

type attachmentCaller interface {
	call(context.Context, string, string, any) (json.RawMessage, error)
}

func syncQueueAttachments(ctx context.Context, remote attachmentCaller, local board.Store, push, pull []board.MissingAttachment) error {
	var failures []error
	for _, missing := range push {
		chunk, err := local.ReadAttachmentChunkSize(missing.Attachment.ID, missing.Offset, 32<<10)
		if err != nil {
			continue
		}
		// The thumbnail already travelled with the request metadata. Repeating
		// its base64 payload in every chunk wastes much of a slow link.
		chunk.Attachment.Thumbnail = ""
		attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, err = remote.call(attempt, "POST", "/v1/attachments/"+chunk.Attachment.ID, chunk)
		cancel()
		if err != nil {
			failures = append(failures, err)
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
	return errors.Join(failures...)
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
	store     board.Store
}

func (e queueMacExecutor) board() board.Store {
	if e.store.Home != "" {
		return e.store
	}
	return board.Store{Home: e.home, Role: "local"}
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
			Cwd      string            `json:"cwd"`
			Title    string            `json:"title"`
			Labels   map[string]string `json:"labels"`
			Status   json.RawMessage   `json:"status"`
			Records  json.RawMessage   `json:"records"`
			StatusMS int64             `json:"status_ms"`
		} `json:"terminals"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, fmt.Errorf("invalid local terminal listing")
	}
	out := []queuescheduler.Session{}
	for _, t := range list.Terminals {
		s := queuescheduler.NewSession(t.Labels["fennec.session"], t.Address, t.State, t.Title, t.Status, t.Records, t.StatusMS)
		s.Cwd = t.Cwd
		out = append(out, s)
	}
	return out, nil
}

// Query calls this Mac's herdr-core (types answers, THI-176).
func (e queueMacExecutor) Query(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return e.core.call(ctx, method, params)
}
func (e queueMacExecutor) End(ctx context.Context, address string) error {
	_, err := e.core.call(ctx, "terminal.kill", map[string]any{"terminal": address})
	return err
}
func (e queueMacExecutor) Start(ctx context.Context, p board.Project, request board.Request, prompt board.Prompt) error {
	terminal, continued, err := e.continueThread(ctx, p.ID, request.Number)
	if err != nil {
		return err
	}
	return queuescheduler.SendRequest(ctx, e.board(), e.core.call, terminal.Address, request, prompt.For(continued))
}
