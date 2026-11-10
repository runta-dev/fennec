package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fennec/internal/launchprofile"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type fenServiceRequest struct {
	Operation string `json:"operation"`
	SessionID string `json:"session_id,omitempty"`
	RuntimeID string `json:"runtime_id,omitempty"`
	Address   string `json:"address,omitempty"`
}

type fenServiceReply struct {
	Plan        *launcher.LaunchPlan    `json:"plan,omitempty"`
	Sessions    []fenSession            `json:"sessions"`
	Receipt     *launcher.LaunchReceipt `json:"receipt,omitempty"`
	ControlPath string                  `json:"control_path,omitempty"`
	Error       string                  `json:"error,omitempty"`
	Refreshing  bool                    `json:"refreshing"`
}

type fenSessionService struct {
	stop       func(context.Context, launcher.LaunchReceipt) error
	resume     func(context.Context, launcher.LaunchReceipt) (launcher.LaunchPlan, error)
	mu         sync.Mutex
	sessions   []fenSession
	listing    json.RawMessage
	list       func(context.Context) ([]byte, error)
	refreshing bool
	updateDone chan struct{}
	problem    string
	generation uint64
	refresh    chan struct{}
	execute    func(context.Context, string) ([]byte, error)
	control    func(context.Context) (string, error)
	runtimeID  string
	home       string
}

func (service *fenSessionService) update(ctx context.Context) {
	service.mu.Lock()
	if service.refreshing {
		done := service.updateDone
		service.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
			}
		}
		return
	}
	service.refreshing = true
	service.updateDone = make(chan struct{})
	done := service.updateDone
	generation := service.generation
	service.mu.Unlock()
	step, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	var data []byte
	var err error
	if service.list != nil {
		data, err = service.list(step)
	} else {
		data, err = service.execute(step, cloudruntime.ListScript)
	}
	var sessions []fenSession
	if err == nil {
		sessions, err = fenParseSessions(data, service.runtimeID)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	defer close(done)
	service.refreshing = false
	if err != nil {
		service.problem = "Cloud session refresh failed; showing the last known list."
		return
	}
	if service.generation != generation {
		select {
		case service.refresh <- struct{}{}:
		default:
		}
		return
	}
	service.sessions = sessions
	service.listing = append(json.RawMessage{}, data...)
	service.problem = ""
}

func (service *fenSessionService) handle(ctx context.Context, request fenServiceRequest) fenServiceReply {
	if request.RuntimeID != "" && request.RuntimeID != service.runtimeID {
		return fenServiceReply{Error: "Session belongs to a different runtime."}
	}
	if request.Operation == "list" || request.Operation == "refresh" {
		if request.Operation == "refresh" {
			select {
			case service.refresh <- struct{}{}:
			default:
			}
		}
		service.mu.Lock()
		defer service.mu.Unlock()
		return fenServiceReply{Sessions: append([]fenSession{}, service.sessions...), Refreshing: service.refreshing, Error: service.problem}
	}
	if (request.Operation != "resume" && request.Operation != "attach" && request.Operation != "kill") || !fenID.MatchString(request.SessionID) {
		return fenServiceReply{Error: "invalid session operation"}
	}
	service.mu.Lock()
	var selected *launcher.LaunchReceipt
	for _, session := range service.sessions {
		if session.Receipt.SessionID == request.SessionID {
			copy := session.Receipt
			selected = &copy
			break
		}
	}
	service.mu.Unlock()
	// UI rows can be newer than the five-second cache. Runtime is still pinned
	// by the daemon and the remote operation verifies the exact session label.
	if selected == nil && request.Operation == "kill" && request.RuntimeID == service.runtimeID && request.Address != "" {
		if _, err := cloudruntime.StopScript(request.Address, request.SessionID); err == nil {
			selected = &launcher.LaunchReceipt{RuntimeID: service.runtimeID, Address: request.Address, SessionID: request.SessionID}
		}
	}
	if selected == nil && (request.Operation == "resume" || request.Operation == "kill") && service.home != "" {
		if receipt, err := launcher.ReadReceipt(filepath.Join(service.home, ".config/fennec/launches"), request.SessionID); err == nil && receipt.RuntimeID == service.runtimeID {
			selected = &receipt
		}
	}
	if selected == nil {
		return fenServiceReply{Error: "Session is not active. Refresh the list before retrying."}
	}
	if request.Operation == "resume" || request.Operation == "attach" {
		if service.resume != nil {
			plan, err := service.resume(ctx, *selected)
			if err != nil {
				return fenServiceReply{Error: err.Error()}
			}
			return fenServiceReply{Receipt: selected, Plan: &plan}
		}
		control, err := service.control(ctx)
		if err != nil {
			return fenServiceReply{Error: "Fennec is reconnecting to the cloud. Try again shortly."}
		}
		return fenServiceReply{Receipt: selected, ControlPath: control}
	}
	defer launchprofile.Begin("session.stop.broker")()
	if service.stop != nil {
		if err := service.stop(ctx, *selected); err != nil {
			return fenServiceReply{Error: "Kill not confirmed; refresh before retrying."}
		}
	} else {
		script, err := cloudruntime.StopScript(selected.Address, selected.SessionID)
		if err != nil {
			return fenServiceReply{Error: err.Error()}
		}
		data, err := service.execute(ctx, script)
		var result struct {
			State string `json:"state"`
		}
		if err != nil || json.Unmarshal(data, &result) != nil || result.State != "finished" {
			return fenServiceReply{Error: "Kill not confirmed; refresh before retrying."}
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.generation++
	remaining := make([]fenSession, 0, len(service.sessions))
	for _, session := range service.sessions {
		if session.Receipt.SessionID != request.SessionID {
			remaining = append(remaining, session)
		}
	}
	service.sessions = remaining
	service.listing = removeStoppedTerminal(service.listing, request.SessionID)
	return fenServiceReply{Sessions: append([]fenSession{}, remaining...)}
}

func startFenSessionService(ctx context.Context, home, runtimeID string, shared *launcher.SharedConnection, channels ...*terminalChannel) (func(), error) {
	directory := filepath.Join(home, ".config/fennec/ipc")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("session service directory must be private")
	}
	lock, err := os.OpenFile(filepath.Join(directory, "sessions.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another session service is active")
	}
	path := filepath.Join(directory, "sessions.sock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			lock.Close()
			return nil, fmt.Errorf("session service path is not a socket")
		}
		if connection, err := net.DialTimeout("unix", path, time.Second); err == nil {
			connection.Close()
			lock.Close()
			return nil, fmt.Errorf("another session service is listening")
		}
		if err := os.Remove(path); err != nil {
			lock.Close()
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		lock.Close()
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	closeService := func() { once.Do(func() { cancel(); listener.Close(); lock.Close() }) }
	context.AfterFunc(ctx, closeService)
	service := &fenSessionService{home: home, runtimeID: runtimeID, refresh: make(chan struct{}, 1), control: shared.Get}
	if len(channels) > 0 {
		channels[0].mu.Lock()
		channels[0].service = service
		channels[0].mu.Unlock()
		service.list = func(operation context.Context) ([]byte, error) {
			transport, err := channels[0].get(operation)
			if err != nil {
				return nil, err
			}
			return transport.Operate(operation, cloudruntime.LocalOperation{Kind: "list"})
		}
		service.stop = func(operation context.Context, receipt launcher.LaunchReceipt) error {
			transport, err := channels[0].get(operation)
			if err != nil {
				return err
			}
			_, err = transport.Stop(operation, cloudruntime.LocalTerminal{ID: receipt.SessionID, Address: receipt.Address, Agent: receipt.Agent})
			return err
		}
		service.resume = func(operation context.Context, receipt launcher.LaunchReceipt) (launcher.LaunchPlan, error) {
			transport, err := channels[0].get(operation)
			if err != nil {
				return launcher.LaunchPlan{}, err
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return launcher.LaunchPlan{}, err
			}
			id := hex.EncodeToString(nonce[:])
			reference := cloudruntime.LocalTerminal{ID: receipt.SessionID, Agent: receipt.Agent, Address: receipt.Address}
			path, err := offerTerminalFrame(ctx, transport, directory, terminalstream.Frame{Type: "attach", ID: id, Terminal: &reference})
			if err != nil {
				return launcher.LaunchPlan{}, err
			}
			return launcher.LaunchPlan{RuntimeID: runtimeID, SessionID: receipt.SessionID, Agent: receipt.Agent, StreamID: id, StreamPath: path, Resume: true}, nil
		}
	}
	service.execute = func(operation context.Context, script string) ([]byte, error) {
		control, err := shared.Get(operation)
		if err != nil {
			return nil, err
		}
		cloud := filepath.Join(home, ".config/fennec/cloud")
		command := exec.CommandContext(operation, "/usr/bin/ssh", "-T", "-S", control, "-o", "ControlMaster=no", "-o", "ProxyCommand=false", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "UserKnownHostsFile="+filepath.Join(cloud, "known_hosts"), "-i", filepath.Join(cloud, "runtime_key"), runtimeID+"@ssh.runta.com", "sh -s")
		command.Stdin = strings.NewReader(script)
		command.WaitDelay = time.Second
		return command.Output()
	}
	go func() {
		service.update(ctx)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				service.update(ctx)
			case <-service.refresh:
				service.update(ctx)
			}
		}
	}()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(40 * time.Second))
				var request fenServiceRequest
				if json.NewDecoder(io.LimitReader(connection, 4096)).Decode(&request) != nil {
					return
				}
				operation, stop := context.WithTimeout(ctx, 35*time.Second)
				defer stop()
				json.NewEncoder(connection).Encode(service.handle(operation, request))
			}()
		}
	}()
	return closeService, nil
}

func fenLocalRequest(home string, request fenServiceRequest) (fenServiceReply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var reply fenServiceReply
	err := (control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}).Call(ctx, "sessions.dispatch", request, &reply)
	if err != nil {
		return reply, err
	}
	if reply.Error != "" && request.Operation != "list" && request.Operation != "refresh" {
		return reply, fmt.Errorf("%s", reply.Error)
	}
	return reply, nil
}

func removeStoppedTerminal(raw json.RawMessage, id string) json.RawMessage {
	var listing map[string]json.RawMessage
	if json.Unmarshal(raw, &listing) != nil {
		return raw
	}
	var rows []map[string]any
	if json.Unmarshal(listing["terminals"], &rows) != nil {
		return raw
	}
	remaining := []map[string]any{}
	for _, row := range rows {
		labels, _ := row["labels"].(map[string]any)
		if labels["fennec.session"] != id {
			remaining = append(remaining, row)
		}
	}
	listing["terminals"], _ = json.Marshal(remaining)
	result, err := json.Marshal(listing)
	if err != nil {
		return raw
	}
	return result
}
func (channel *terminalChannel) cachedListing(refresh bool) json.RawMessage {
	channel.mu.Lock()
	service := channel.service
	channel.mu.Unlock()
	if service == nil {
		return json.RawMessage(`{"terminals":[]}`)
	}
	if refresh {
		select {
		case service.refresh <- struct{}{}:
		default:
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.listing) == 0 {
		return json.RawMessage(`{"terminals":[]}`)
	}
	return append(json.RawMessage{}, service.listing...)
}
