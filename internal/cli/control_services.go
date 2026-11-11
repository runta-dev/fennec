package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fennec/internal/localssh"
	"fennec/internal/runta"
	"fennec/internal/terminalstream"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// daemonServices owns the account-scoped processes. UI clients never own these
// contexts, pipes, credentials, preferences or reconnect timers.
type daemonServices struct {
	nativeLoginMu                              sync.Mutex
	loginObserver                              func(context.Context, string, []byte)
	nativeAccountMu                            sync.Mutex
	nativeAccountCache                         map[string]cachedNativeAccount
	nativeAccountPending                       map[string]chan struct{}
	channel                                    *terminalChannel
	initializeMu                               sync.Mutex
	user                                       string
	mu                                         sync.Mutex
	home                                       string
	account                                    *account.Manager
	epoch                                      uint64
	ctx                                        context.Context
	cancel                                     context.CancelFunc
	workers                                    sync.WaitGroup
	config                                     brokerConfiguration
	selected                                   map[string]bool
	ready                                      map[string]bool
	brokerRunning, brokerReady, connectorReady bool
	connectorAddress                           string
	connectorInput                             *io.PipeWriter
	decisionMu                                 sync.Mutex
	linkCancel                                 context.CancelFunc
	linkAddress                                string
	approvals                                  map[string]localssh.Event
	approvalOrder                              []string
	status                                     map[string]string
	coverage                                   map[string]string
	entryQueue                                 chan string
	entryPending                               map[string]bool
	entryInstaller                             func(context.Context, string) (string, error)
	trustWait                                  chan bool
	permissions                                *localssh.Broker
	hookLeases                                 map[string]hookLease
}

func newDaemonServices(home string, account *account.Manager) *daemonServices {
	executable, _ := os.Executable()
	return &daemonServices{home: home, account: account, entryInstaller: func(ctx context.Context, id string) (string, error) {
		return installManagedEntryAt(ctx, executable, id)
	}}
}
func writePublicJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// user is supplied only from EnsureShared's freshly verified runtime owner.
func (s *daemonServices) initialize(lifecycle context.Context, token string, epoch uint64, user string) error {
	s.initializeMu.Lock()
	defer s.initializeMu.Unlock()
	if lifecycle.Err() != nil {
		return lifecycle.Err()
	}
	if user == "" {
		return fmt.Errorf("verified account identity required")
	}
	s.mu.Lock()
	if s.epoch == epoch && s.ctx != nil {
		matches := s.user == user
		s.mu.Unlock()
		if !matches {
			return fmt.Errorf("account identity changed within lifecycle")
		}
		return nil
	}
	old := s.cancel
	s.mu.Unlock()
	if old != nil {
		old()
		s.workers.Wait()
	}
	directory := filepath.Join(s.home, ".config/fennec/ssh")
	if err := localssh.BindOwner(directory, user); err != nil {
		return err
	}
	deviceFile := filepath.Join(directory, "device_id")
	device, err := os.ReadFile(deviceFile)
	if os.IsNotExist(err) {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		device = []byte(hex.EncodeToString(nonce[:]))
		err = os.WriteFile(deviceFile, device, 0600)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(lifecycle)
	s.mu.Lock()
	s.epoch = epoch
	s.user = user
	s.ctx = ctx
	s.cancel = cancel
	s.config = brokerConfiguration{Token: token, Device: strings.TrimSpace(string(device)), Socket: filepath.Join(s.home, ".config/fennec/ipc/launch.sock"), AutoProjects: true}
	s.selected = map[string]bool{}
	s.ready = map[string]bool{}
	s.approvals = map[string]localssh.Event{}
	s.hookLeases = map[string]hookLease{}
	s.approvalOrder = nil
	s.status = map[string]string{"stage": "connecting"}
	s.coverage = map[string]string{}
	s.entryQueue = make(chan string, 8)
	s.entryPending = map[string]bool{}
	s.brokerRunning = false
	s.channel = nil
	s.brokerReady = false
	s.connectorReady = false
	s.connectorAddress = ""
	s.connectorInput = nil
	s.linkAddress = ""
	s.linkCancel = nil
	s.mu.Unlock()
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.runConnector(ctx, epoch) }()
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.runEntryRepair(ctx, epoch) }()
	return nil
}
func (s *daemonServices) prepare(ctx context.Context, token string, output io.Writer) error {
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return fmt.Errorf("login required")
	}
	if handled, err := s.reconfigureExisting(ctx, epoch, output); handled {
		return err
	}
	configurationMode := newDesktopSettings(s.home).value.Magpie
	events := &serviceEvents{emit: func(event map[string]json.RawMessage) {
		if ctx.Err() == nil && newDesktopSettings(s.home).value.Magpie == configurationMode {
			s.runtimeEvent(epoch, event)
		}
	}}
	return prepareRuntime(ctx, token, io.MultiWriter(output, events), preparationRetainer{Runtime: func(runtime runta.Runtime) error {
		return s.initialize(lifecycle, token, epoch, runtime.Owner)
	}, Connection: func(runtimeID string, connection *ssh.Client) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.epoch != epoch || s.ctx == nil || s.ctx.Err() != nil || s.brokerRunning {
			return false
		}
		if s.config.PreparedSSH != nil {
			s.config.PreparedSSH.Close()
		}
		s.config.PreparedSSH = connection
		s.config.PreparedTerminal = nil
		s.config.PreparedRuntimeID = runtimeID
		owner := s.ctx
		s.workers.Add(1)
		go func() { defer s.workers.Done(); <-owner.Done(); connection.Close() }()
		return true
	}, Terminal: func(runtimeID string, transport *terminalstream.Transport) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.epoch != epoch || s.ctx == nil || s.ctx.Err() != nil || s.brokerRunning || s.config.PreparedRuntimeID != runtimeID {
			return false
		}
		s.config.PreparedTerminal = transport
		return true
	}})
}
func decodeText(event map[string]json.RawMessage, key string) string {
	var value string
	json.Unmarshal(event[key], &value)
	return value
}
func (s *daemonServices) runtimeEvent(epoch uint64, event map[string]json.RawMessage) {
	kind := decodeText(event, "type")
	runtimeID := decodeText(event, "runtime_id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch != epoch || s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	if runtimeID != "" && s.config.RuntimeID == "" {
		s.config.RuntimeID = runtimeID
		var saved struct {
			RuntimeID string   `json:"runtime_id"`
			Agents    []string `json:"agents"`
			Roots     []string `json:"roots"`
		}
		if data, err := os.ReadFile(filepath.Join(s.home, ".config/fennec/takeover-preferences.json")); err == nil && json.Unmarshal(data, &saved) == nil && saved.RuntimeID == runtimeID {
			for _, id := range saved.Agents {
				if launcher.Supported[id] {
					s.selected[id] = true
				}
			}
			s.config.Roots = saved.Roots
		}
	}
	switch kind {
	case "connector_ready":
		s.connectorReady = true
		s.startLinkLocked()
	case "runtime_progress":
		_ = writePublicJSON(filepath.Join(s.home, ".config/fennec/preparation.json"), event)
	case "route_results":
		var routes []struct {
			Agent  string `json:"agent"`
			Status string `json:"status"`
		}
		data := []byte(decodeText(event, "results"))
		if json.Unmarshal(data, &routes) == nil {
			s.ready = map[string]bool{}
			for _, route := range routes {
				if route.Status == "configured" {
					s.ready[route.Agent] = true
				}
			}
			_ = writePublicJSON(filepath.Join(s.home, ".config/fennec/route-results.json"), json.RawMessage(data))
		}
	case "agent_routes":
		data := json.RawMessage(decodeText(event, "routes"))
		if json.Valid(data) {
			_ = writePublicJSON(filepath.Join(s.home, ".config/fennec/agent-routes.json"), data)
		}
	}
	s.reconcileSelectionLocked()
	if kind == "shared_services_ready" && !s.brokerRunning && s.config.RuntimeID != "" {
		s.brokerRunning = true
		ctx := s.ctx
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			delay := time.Second
			for ctx.Err() == nil {
				events := &serviceEvents{emit: func(event map[string]json.RawMessage) {
					s.mu.Lock()
					defer s.mu.Unlock()
					if s.epoch != epoch {
						return
					}
					switch decodeText(event, "type") {
					case "broker_ready":
						s.brokerReady = true
					case "entry_coverage":
						s.coverage[decodeText(event, "agent")] = decodeText(event, "coverage")
					}
				}}
				_ = serveLaunchBroker(ctx, s.home, func() brokerConfiguration { s.mu.Lock(); defer s.mu.Unlock(); return s.config }, events, func(channel *terminalChannel) {
					s.mu.Lock()
					defer s.mu.Unlock()
					if s.epoch == epoch {
						s.channel = channel
						channel.mu.Lock()
						channel.onListing = func(ctx context.Context, data []byte) {
							s.mu.Lock()
							observe := s.loginObserver
							s.mu.Unlock()
							if observe != nil {
								observe(ctx, channel.runtimeID, data)
							}
						}
						channel.mu.Unlock()
					}
				})
				s.mu.Lock()
				if s.epoch == epoch {
					s.brokerReady = false
					s.channel = nil
				}
				s.mu.Unlock()
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				delay = min(delay*2, 30*time.Second)
			}
		}()
	}
}
func (s *daemonServices) reconcileSelectionLocked() {
	if s.config.RuntimeID == "" {
		return
	}
	selected, active := []string{}, []string{}
	for id := range s.selected {
		selected = append(selected, id)
		s.queueEntryLocked(id)
		if s.ready[id] {
			active = append(active, id)
		}
	}
	sort.Strings(selected)
	sort.Strings(active)
	s.config.Selected = selected
	s.config.Agents = active
	_ = launcher.PublishEnabled(filepath.Join(s.home, ".config/fennec/takeover.json"), selected)
}
func (s *daemonServices) runConnector(ctx context.Context, epoch uint64) {
	delay := time.Second
	for ctx.Err() == nil {
		input, writer := io.Pipe()
		stop := context.AfterFunc(ctx, func() { writer.Close(); input.Close() })
		s.mu.Lock()
		s.connectorInput = writer
		s.mu.Unlock()
		events := &serviceEvents{emit: func(raw map[string]json.RawMessage) {
			data, _ := json.Marshal(raw)
			var event localssh.Event
			if json.Unmarshal(data, &event) != nil {
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.epoch != epoch || ctx.Err() != nil {
				return
			}
			switch event.Type {
			case "listening":
				s.connectorAddress = event.Address
				s.startLinkLocked()
			case "approval":
				if _, exists := s.approvals[event.ID]; !exists {
					s.approvalOrder = append(s.approvalOrder, event.ID)
				}
				s.approvals[event.ID] = event
			case "dismiss":
				delete(s.approvals, event.ID)
				for i, id := range s.approvalOrder {
					if id == event.ID {
						s.approvalOrder = append(s.approvalOrder[:i], s.approvalOrder[i+1:]...)
						break
					}
				}
			case "trust_cleared", "trust_clear_failed":
				s.status["trust_result"] = event.Type
				if s.trustWait != nil {
					select {
					case s.trustWait <- event.Type == "trust_cleared":
					default:
					}
				}
			}
		}}
		permissions := localssh.NewBroker(filepath.Join(s.home, ".config/fennec/ssh"), events)
		s.mu.Lock()
		s.permissions = permissions
		s.mu.Unlock()
		_ = localssh.RunWithApproval(ctx, filepath.Join(s.home, ".config/fennec/ssh"), input, events, true, permissions)
		stop()
		writer.Close()
		input.Close()
		s.mu.Lock()
		s.connectorInput = nil
		s.permissions = nil
		s.connectorAddress = ""
		s.approvals = map[string]localssh.Event{}
		s.approvalOrder = nil
		if s.linkCancel != nil {
			s.linkCancel()
		}
		s.linkAddress = ""
		s.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}
func (s *daemonServices) startLinkLocked() {
	if !s.connectorReady || s.connectorAddress == "" || s.config.RuntimeID == "" || s.linkAddress == s.connectorAddress {
		return
	}
	if s.linkCancel != nil {
		s.linkCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.linkCancel = cancel
	s.linkAddress = s.connectorAddress
	epoch := s.epoch
	token := s.config.Token
	name, _ := os.Hostname()
	config := localssh.LinkConfig{RuntimeID: s.config.RuntimeID, LocalAddress: s.connectorAddress, Name: name, Directory: filepath.Join(s.home, ".config/fennec/ssh")}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		delay := time.Second
		for ctx.Err() == nil {
			_ = localssh.Link(ctx, &runta.Client{Endpoint: "https://api.runta.com", Token: token}, config, func(stage, detail string) {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.epoch != epoch || ctx.Err() != nil {
					return
				}
				s.status["stage"] = stage
				s.status["detail"] = detail
				_ = writePublicJSON(filepath.Join(s.home, ".config/fennec/connector-status.json"), s.status)
			})
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(delay*2, 30*time.Second)
		}
	}()
}
func (s *daemonServices) snapshot() any {
	account := s.account.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	if account.Epoch != s.epoch {
		return map[string]any{"broker_ready": false, "approvals": []localssh.Event{}}
	}
	approvals := []localssh.Event{}
	for _, id := range s.approvalOrder {
		if event, ok := s.approvals[id]; ok {
			approvals = append(approvals, event)
		}
	}
	status := map[string]string{}
	for k, v := range s.status {
		status[k] = v
	}
	coverage := map[string]string{}
	for k, v := range s.coverage {
		coverage[k] = v
	}
	state := map[string]any{"broker_ready": s.brokerReady, "selection_known": s.config.RuntimeID != "", "selected": append([]string{}, s.config.Selected...), "active": append([]string{}, s.config.Agents...), "connector": status, "approvals": approvals, "coverage": coverage}
	if s.channel != nil {
		state["sessions"] = s.channel.cachedListing(false)
	}
	return state
}
func (s *daemonServices) decide(ctx context.Context, raw json.RawMessage) (any, error) {
	_, _, epoch, authorized := s.account.Session()
	if !authorized {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
	}
	var decision localssh.Decision
	if json.Unmarshal(raw, &decision) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid approval decision"}
	}
	if decision.Action != "" && decision.Action != "clear_trust" {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid approval action"}
	}
	s.decisionMu.Lock()
	defer s.decisionMu.Unlock()
	s.mu.Lock()
	writer := s.connectorInput
	_, pending := s.approvals[decision.ID]
	if s.epoch != epoch || s.ctx == nil || s.ctx.Err() != nil {
		writer = nil
	}
	confirmation := make(chan bool, 1)
	if decision.Action == "clear_trust" {
		s.trustWait = confirmation
	} else if pending {
		delete(s.approvals, decision.ID)
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.trustWait == confirmation {
			s.trustWait = nil
		}
		s.mu.Unlock()
	}()
	if writer == nil || decision.Action != "clear_trust" && !pending {
		return nil, &control.Fault{Code: "approval_expired", Message: "Approval is no longer pending"}
	}
	result := make(chan error, 1)
	go func() { result <- json.NewEncoder(writer).Encode(decision) }()
	select {
	case err := <-result:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if decision.Action == "clear_trust" {
		select {
		case success := <-confirmation:
			if !success {
				return nil, &control.Fault{Code: "trust_clear_failed", Message: "Unable to clear device trust"}
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return map[string]bool{"accepted": true}, nil
}

type serviceEvents struct {
	mu      sync.Mutex
	pending []byte
	emit    func(map[string]json.RawMessage)
}

func (w *serviceEvents) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending)+len(data) > 4<<20 {
		return 0, fmt.Errorf("event too large")
	}
	w.pending = append(w.pending, data...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(w.pending[:index], &event) != nil {
			return 0, fmt.Errorf("invalid service event")
		}
		w.pending = w.pending[index+1:]
		w.emit(event)
	}
	return len(data), nil
}

func (s *daemonServices) close() {
	s.initializeMu.Lock()
	defer s.initializeMu.Unlock()
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.workers.Wait()
}
