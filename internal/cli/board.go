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
	s *daemonServices
}

func (b boardClient) dial(ctx context.Context) (net.Conn, error) {
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

// do sends one request and returns the status and JSON body.
func (b boardClient) do(ctx context.Context, method, path string, body any) (int, json.RawMessage, error) {
	if !boardPath.MatchString(path) || strings.Contains(path, "..") || (method != http.MethodGet && method != http.MethodPost && method != http.MethodPatch) {
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
		return nil, &control.Fault{Code: "board_failed", Message: "The cloud computer could not save this", Retryable: true}
	}
	return data, nil
}

func installBoard(ctx context.Context, s *daemonServices, c *controlplane.Controller) {
	client := boardClient{s: s}
	c.Additional["board.call"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
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
		return client.call(ctx, params.Method, params.Path, body)
	}
	go (&boardQueue{s: s, c: c, board: client}).run(ctx)
}

// boardQueue hands queued requests to agents: a new session through the
// same cloud launch as a session started from the iPhone, or the new
// messages to the request's live session.
type boardQueue struct {
	s     *daemonServices
	c     *controlplane.Controller
	board boardClient
}

func (q *boardQueue) run(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, _, _, ok := q.s.account.Session(); !ok {
			continue
		}
		q.s.mu.Lock()
		ready := q.s.channel != nil && q.s.brokerReady
		q.s.mu.Unlock()
		if ready {
			q.pass(ctx)
		}
	}
}

func (q *boardQueue) pass(ctx context.Context) {
	status, data, err := q.board.do(ctx, http.MethodGet, "/v1/queue", nil)
	if err != nil || status != http.StatusOK {
		return
	}
	var projects []board.QueueProject
	if json.Unmarshal(data, &projects) != nil {
		return
	}
	live := q.liveSessions(ctx)
	started := 0
	for _, project := range projects {
		slots := project.Limit - project.Working
		for _, request := range project.Queued {
			if slots <= 0 || started >= 2 || ctx.Err() != nil {
				break
			}
			if q.take(ctx, project, request, live) {
				slots--
				started++
			}
		}
	}
}

// liveSessions are the sessions on the cloud computer that are still running.
func (q *boardQueue) liveSessions(ctx context.Context) map[string]bool {
	live := map[string]bool{}
	result, err := q.c.Handlers()["sessions.list"](ctx, nil)
	if err != nil {
		return live
	}
	raw, _ := json.Marshal(result)
	var listing struct {
		Terminals []struct {
			State  string            `json:"state"`
			Labels map[string]string `json:"labels"`
		} `json:"terminals"`
	}
	if json.Unmarshal(raw, &listing) != nil {
		return live
	}
	for _, terminal := range listing.Terminals {
		if id := terminal.Labels["fennec.session"]; id != "" && terminal.State != "finished" && terminal.State != "exited" {
			live[id] = true
		}
	}
	return live
}

func (q *boardQueue) take(ctx context.Context, project board.QueueProject, request board.Request, live map[string]bool) bool {
	path := fmt.Sprintf("/v1/projects/%s/requests/%d", project.Project, request.Number)
	if request.Session != "" && live[request.Session] {
		// A reply to a request its agent is still on: pass the new messages on.
		var text strings.Builder
		for _, message := range request.Messages {
			if message.ID > request.Delivered && message.Agent == "" {
				fmt.Fprintf(&text, "New reply on %s-%d from %s:\n%s\n\n", project.Key, request.Number, message.Author, message.Body)
			}
		}
		if text.Len() == 0 {
			return false
		}
		fmt.Fprintf(&text, "Answer with `fen reply %s-%d` when you are done.", project.Key, request.Number)
		if status, _, err := q.board.do(ctx, http.MethodPost, path+"/claim", map[string]string{"session": request.Session, "agent": request.Agent}); err != nil || status != http.StatusOK {
			return false
		}
		message := text.String()
		if len(message) > 16000 {
			message = message[:16000] + fmt.Sprintf("\n\n(Cut short; `fen request %s-%d` has the whole thread.)", project.Key, request.Number)
		}
		params, _ := json.Marshal(controlplane.SessionParams{SessionID: request.Session, Text: message})
		if _, err := q.c.Handlers()["sessions.send"](ctx, params); err != nil {
			q.board.do(ctx, http.MethodPost, path+"/release", map[string]string{"session": request.Session})
		}
		return true
	}
	var worker *board.Worker
	for i := range project.Workers {
		if project.Workers[i].On {
			worker = &project.Workers[i]
			break
		}
	}
	if worker == nil {
		return false
	}
	id, err := sessionid.Allocate(q.s.home)
	if err != nil {
		return false
	}
	if status, _, err := q.board.do(ctx, http.MethodPost, path+"/claim", map[string]string{"session": id, "agent": worker.Agent}); err != nil || status != http.StatusOK {
		return false
	}
	p, err := q.project(ctx, project.Project)
	if err == nil {
		err = q.launch(ctx, id, worker.Agent, launchArguments(*worker, firstMessage(p, request, worker.Agent)))
	}
	if err != nil {
		q.board.do(ctx, http.MethodPost, path+"/release", map[string]string{"session": id})
		return false
	}
	return true
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

// firstMessage is what a new session for a request is asked to do.
func firstMessage(p board.Project, request board.Request, agent string) string {
	ref := fmt.Sprintf("%s-%d", p.Key, request.Number)
	var b strings.Builder
	fmt.Fprintf(&b, "You are working on request %s from the %s project board in Fennec.\n\n", ref, p.Name)
	b.WriteString(board.Thread(p, request))
	b.WriteString("\nHow to work on it:\n")
	if p.Repository != "" {
		fmt.Fprintf(&b, "- The code is github.com/%s. Use a checkout under ~/repos/%s (clone it if missing).\n", p.Repository, p.Repository)
	}
	fmt.Fprintf(&b, "- When you have a result, a question, or something for the person to check, answer with `fen reply %s \"...\"` (or `fen reply %s -` with the text on standard input). Replying hands the request back to them; their next reply reaches you here.\n", ref, ref)
	fmt.Fprintf(&b, "- `fen request %s` prints the whole thread.\n", ref)
	fmt.Fprintf(&b, "- Record what a later agent should know about this project (architecture, runbooks, decisions, terms) as OKF concepts: markdown files under ~/.fen/board/projects/%s/knowledge/<section>/<name>.md with YAML front matter (type, title, description, tags, sources: [%s], generated: {by: %s, at: <UTC time>}, status: draft). Keep each to one idea.\n", p.ID, ref, agent)
	return b.String()
}
