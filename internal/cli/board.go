package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"fennec/internal/board"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	r "fennec/internal/runtime"
	"fennec/internal/sessionid"
	"fennec/internal/terminalstream"
)

// boardClient reaches the cloud computer's board (projects, requests,
// knowledge) over the daemon's authenticated resident connection, the way
// previews are reached. See docs/desktop-app.md.
type boardClient struct {
	s     *daemonServices
	local bool
}

func (b boardClient) dial(ctx context.Context) (net.Conn, error) {
	if b.local {
		return (board.Store{Home: b.s.home, Role: "local"}).Dial(ctx)
	}
	if _, _, _, ok := b.s.account.Session(); !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Fennec"}
	}
	b.s.mu.Lock()
	channel := b.s.channel
	b.s.mu.Unlock()
	if channel == nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if err = transport.Ready(ctx); err != nil {
		return nil, err
	}
	if !transport.Supports(terminalstream.CapabilityBoard) {
		return nil, &control.Fault{Code: "not_ready", Message: "The cloud computer is updating", Retryable: true}
	}
	return transport.DialSocket(ctx, terminalstream.SocketBoard, "")
}

var boardPath = regexp.MustCompile(`^/v1/[A-Za-z0-9/_.-]{0,200}$`)

// knowledgePath matches a project's knowledge endpoints. The bundle is files
// agents write on the cloud computer; replication does not carry it, so the
// Mac's local board never has it. It is always read and reviewed in the cloud.
var knowledgePath = regexp.MustCompile(`^/v1/projects/[^/]+/knowledge(?:/|$)`)

// forPath sends knowledge requests to the cloud board even from a local client.
func (b boardClient) forPath(path string) boardClient {
	if b.local && knowledgePath.MatchString(path) {
		b.local = false
	}
	return b
}

// do sends one request and returns the status and JSON body.
func (b boardClient) do(ctx context.Context, method, path string, body any) (int, json.RawMessage, error) {
	if !boardPath.MatchString(path) || strings.Contains(path, "..") || (method != http.MethodGet && method != http.MethodPost && method != http.MethodPatch && method != http.MethodDelete) {
		return 0, nil, &control.Fault{Code: "invalid_params", Message: "Invalid board request"}
	}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return b.dial(ctx) },
		DisableKeepAlives: true,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, method, "http://board.fennec"+path, payload)
	if err != nil {
		return 0, nil, err
	}
	request.Host = "board.fennec"
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		if fault, ok := err.(*control.Fault); ok {
			return 0, nil, fault
		}
		var unwrapped *control.Fault
		if errors.As(err, &unwrapped) {
			return 0, nil, unwrapped
		}
		return 0, nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, data, nil
}

// call is do for the app: errors become faults the page can show.
func (b boardClient) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	status, data, err := b.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, &control.Fault{Code: "not_found", Message: "Not found"}
	case status == http.StatusConflict:
		return nil, &control.Fault{Code: "conflict", Message: "Changed elsewhere; refresh"}
	case status >= 400 && status < 500:
		return nil, &control.Fault{Code: "invalid_params", Message: "Check the values and try again"}
	case status >= 500:
		return nil, &control.Fault{Code: "board_failed", Message: "Could not save this", Retryable: true}
	}
	return data, nil
}

func installBoard(ctx context.Context, s *daemonServices, c *controlplane.Controller) {
	client := boardClient{s: s}
	c.Additional["board.attachment.url"] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Choose an image"}
		}
		url, err := (board.Store{Home: s.home, Role: "local"}).AttachmentURL(params.ID)
		if err != nil {
			return nil, &control.Fault{Code: "not_ready", Message: "The image is still syncing", Retryable: true}
		}
		return map[string]string{"url": url}, nil
	}
	imageImports := make(chan struct{}, 2)
	c.Additional["board.attachment.import"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		select {
		case imageImports <- struct{}{}:
			defer func() { <-imageImports }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var params struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &params) != nil || !filepath.IsAbs(params.Path) {
			return nil, &control.Fault{Code: "invalid_params", Message: "Choose an image"}
		}
		attachment, err := (board.Store{Home: s.home, Role: "local"}).ImportAttachment(params.Path)
		if err != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Use a PNG or JPEG image up to 8 MB"}
		}
		return attachment, nil
	}
	c.Additional["board.icon"] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Choose an image"}
		}
		icon, err := projectIcon(params.Path)
		if err != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Choose a PNG or JPEG image"}
		}
		return map[string]string{"icon": icon}, nil
	}
	for _, method := range []string{"board.call", "board.local.call"} {
		boardService := boardClient{s: s, local: method == "board.local.call"}
		c.Additional[method] = func(ctx context.Context, raw json.RawMessage) (any, error) {
			var params struct {
				Method string          `json:"method"`
				Path   string          `json:"path"`
				Body   json.RawMessage `json:"body,omitempty"`
			}
			if json.Unmarshal(raw, &params) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Invalid board request"}
			}
			var body any
			if len(params.Body) > 0 && string(params.Body) != "null" {
				body = params.Body
			}
			return boardService.forPath(params.Path).call(ctx, params.Method, params.Path, body)
		}
	}
	c.Additional["account.organizations"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return s.account.Organizations(ctx)
	}
	queue := &boardQueue{s: s, c: c, board: client}
	c.Additional["board.session"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			Project string `json:"project"`
			Number  int    `json:"number"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return nil, board.ErrInvalid
		}
		op := r.LocalOperation{Kind: "thread_session", Project: params.Project, RequestNumber: params.Number}
		if err := op.Validate(); err != nil {
			return nil, err
		}
		localStore := board.Store{Home: s.home, Role: "local"}
		if request, err := localStore.Request(params.Project, params.Number); err == nil && len(request.Runs) > 0 && request.Runs[len(request.Runs)-1].Location == "local" {
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			s.mu.Lock()
			core := s.boardCore
			s.mu.Unlock()
			if core == nil {
				return nil, &control.Fault{Code: "not_ready", Message: "Session service is starting", Retryable: true}
			}
			terminal, err := (queueMacExecutor{home: s.home, exe: exe, core: core}).threadSession(ctx, params.Project, params.Number)
			if err != nil {
				return nil, err
			}
			return map[string]any{"terminal": terminal, "location": "local"}, nil
		}
		s.mu.Lock()
		channel := s.channel
		s.mu.Unlock()
		if channel == nil {
			return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
		}
		transport, err := channel.get(ctx)
		if err != nil {
			return nil, err
		}
		if err := transport.Ready(ctx); err != nil {
			return nil, err
		}
		if !transport.Supports(terminalstream.CapabilityThreadSession) {
			return nil, &control.Fault{Code: "not_ready", Message: "The cloud computer is updating", Retryable: true}
		}
		rawTerminal, err := transport.Operate(ctx, op)
		if err != nil {
			return nil, err
		}
		return map[string]any{"terminal": json.RawMessage(rawTerminal), "location": "cloud"}, nil
	}
	// sessions.local_folder gives the desktop window the working directory
	// for an agent session it starts on this Mac ("" keeps the home folder).
	c.Additional["sessions.local_folder"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			Agent string `json:"agent"`
			Name  string `json:"name"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid session"}
		}
		dir, err := desktopLocalFolder(ctx, s.home, params.Agent, params.Name)
		if err != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "The session folder could not be prepared"}
		}
		return map[string]string{"directory": dir}, nil
	}
	// sessions.start opens a session on the cloud computer from the desktop
	// window's New session: no Mac folder, optionally in a project.
	c.Additional["sessions.start"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			Agent   string `json:"agent"`
			Model   string `json:"model"`
			Project string `json:"project"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &params) != nil || !board.Agents[params.Agent] || len(params.Message) > 16000 || len(params.Model) > 80 || strings.ContainsAny(params.Model, "\x00\r\n") {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid session"}
		}
		message := strings.TrimSpace(params.Message)
		if params.Project != "" {
			p, err := queue.project(ctx, params.Project)
			if err != nil {
				return nil, &control.Fault{Code: "not_found", Message: "Project not found"}
			}
			intro := fmt.Sprintf("You are working in the %s project", p.Name)
			if p.Repository != "" {
				intro += fmt.Sprintf(" (github.com/%s; use a checkout under ~/repos/%s, cloning it if missing)", p.Repository, p.Repository)
			}
			intro += fmt.Sprintf(". What earlier agents learned about it is in ~/.fen/board/projects/%s/knowledge/.", p.ID)
			if message == "" {
				message = intro + " Wait for instructions."
			} else {
				message = intro + "\n\n" + message
			}
		}
		id, err := sessionid.Allocate(s.home)
		if err != nil {
			return nil, err
		}
		var arguments []string
		if message != "" {
			arguments = launchArguments(board.Worker{Agent: params.Agent, Model: params.Model}, message)
		} else if params.Model != "" {
			arguments = launchArguments(board.Worker{Agent: params.Agent, Model: params.Model}, "")
			arguments = arguments[:len(arguments)-1]
		}
		if err = queue.launch(ctx, id, params.Agent, arguments); err != nil {
			return nil, &control.Fault{Code: "launch_failed", Message: "The session could not start", Retryable: true}
		}
		return map[string]string{"session_id": id}, nil
	}
	startQueueReplication(ctx, s)
}

// boardQueue hands queued requests to agents: a new session through the
// same cloud launch as a session started from the iPhone, or the new
// messages to the request's live session.
type boardQueue struct {
	s     *daemonServices
	c     *controlplane.Controller
	board boardClient
}

func (q *boardQueue) project(ctx context.Context, id string) (board.Project, error) {
	var p board.Project
	status, data, err := q.board.do(ctx, http.MethodGet, "/v1/projects/"+id, nil)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("project unavailable")
	}
	if err == nil {
		err = json.Unmarshal(data, &p)
	}
	return p, err
}

// launch starts the agent on the cloud computer with no Mac folder, as a
// session started from the iPhone does, and records its receipt so the
// session appears in Fennec and `fen attach` reaches it.
func (q *boardQueue) launch(ctx context.Context, id, agent string, arguments []string) error {
	q.s.mu.Lock()
	channel := q.s.channel
	config := q.s.config
	q.s.mu.Unlock()
	if channel == nil {
		return fmt.Errorf("not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	transport, err := channel.get(ctx)
	if err != nil {
		return err
	}
	raw, err := transport.Operate(ctx, r.LocalOperation{Kind: "launch", Launch: &r.LocalLaunch{ID: id, Agent: agent, LocalDirectory: "/", Device: config.Device, Arguments: arguments}})
	if err != nil {
		return err
	}
	var terminal r.LocalTerminal
	if json.Unmarshal(raw, &terminal) != nil || terminal.ID != id || terminal.Address == "" {
		return fmt.Errorf("invalid launch receipt")
	}
	receipt := launcher.LaunchReceipt{RuntimeID: config.RuntimeID, SessionID: id, Agent: agent, Address: terminal.Address}
	if err = launcher.SaveReceipt(filepath.Join(q.s.home, ".config/fennec/launches"), receipt); err != nil {
		return err
	}
	channel.cachedListing(true)
	return nil
}

// launchArguments are the agent's command-line arguments: the worker's model
// where the agent takes one, then the first message.
func launchArguments(worker board.Worker, message string) []string {
	var arguments []string
	if worker.Model != "" && (worker.Agent == "claude" || worker.Agent == "codex") {
		arguments = append(arguments, "--model", worker.Model)
	}
	return append(arguments, message)
}
