// Package mcpserver serves MCP JSON-RPC over a line-delimited stream (stdio or
// a unix socket connection). It handles the handshake, tools/list, tools/call
// and cancellation; tools are supplied by a Handler. A Handler that is also
// a ResourceHandler serves resources/list and resources/read.
package mcpserver

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
)

// Supported protocol versions, newest first.
var Versions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// JSON-RPC error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// MaxMessage bounds one JSON-RPC message.
const MaxMessage = 64 << 20

// Tool describes one tool for tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Image is an image content block.
type Image struct {
	Data     string // base64
	MIMEType string
}

// Result is a tool call's answer.
type Result struct {
	Text       string
	Images     []Image
	IsError    bool
	Structured any
}

// Session is one MCP connection: one agent.
type Session struct {
	// AgentID identifies the agent for per-agent state.
	AgentID string
	// Client is the client name from initialize.
	Client string
	// Version is the negotiated protocol version.
	Version string
	// RunToken is the initialize request's _meta["fennec-cua/run"]: a
	// script plugin passes it so its calls pass as its run's caller.
	RunToken string
}

// Handler answers tools.
type Handler interface {
	Tools(s *Session) []Tool
	Call(ctx context.Context, s *Session, name string, args json.RawMessage) Result
}

// Info describes the server for initialize.
type Info struct {
	Name         string
	Version      string
	Instructions string
	// IDPrefix starts every agent id, such as "mcp" or "mcp-http".
	IDPrefix string
	// FixedID, when set, replaces the generated agent id part after the
	// prefix (the HTTP session id).
	FixedID string
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests from r and writes replies to w until r ends or ctx is
// done. Tool calls run concurrently; replies keep request order. When the
// stream ends, calls still running are cancelled.
func Serve(ctx context.Context, r io.Reader, w io.Writer, h Handler, info Info) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{h: h, info: info, w: w, running: map[string]context.CancelFunc{}}
	s.sess.AgentID = agentID(info.IDPrefix, "", info.FixedID)
	out := make(chan *pending, 1024)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for p := range out {
			<-p.done
			if p.reply != nil {
				s.write(p.reply)
			}
		}
	}()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), MaxMessage)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if p := s.dispatch(ctx, []byte(line), &wg); p != nil {
			out <- p
		}
	}
	err := sc.Err()
	cancel()
	wg.Wait()
	close(out)
	<-writerDone
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return err
}

type pending struct {
	done  chan struct{}
	reply *reply
}

func ready(r *reply) *pending {
	p := &pending{done: make(chan struct{}), reply: r}
	close(p.done)
	return p
}

type server struct {
	h    Handler
	info Info
	sess Session

	wmu sync.Mutex
	w   io.Writer

	rmu     sync.Mutex
	running map[string]context.CancelFunc
}

func (s *server) write(r *reply) {
	r.JSONRPC = "2.0"
	data, err := json.Marshal(r)
	if err != nil {
		data, _ = json.Marshal(reply{JSONRPC: "2.0", ID: r.ID, Error: &rpcError{Code: CodeInternal, Message: err.Error()}})
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.w.Write(append(data, '\n'))
}

func fail(id json.RawMessage, code int, msg string) *pending {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return ready(&reply{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *server) dispatch(ctx context.Context, line []byte, wg *sync.WaitGroup) *pending {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return fail(nil, CodeParse, "parse error: "+err.Error())
	}
	notification := len(m.ID) == 0 || string(m.ID) == "null"
	if m.Method == "" {
		if notification {
			return nil // a stray response; nothing to answer
		}
		return fail(m.ID, CodeInvalidRequest, "request has no method")
	}
	if notification {
		s.notify(m)
		return nil
	}
	switch m.Method {
	case "initialize":
		return ready(&reply{ID: m.ID, Result: s.initialize(m.Params)})
	case "ping":
		return ready(&reply{ID: m.ID, Result: map[string]any{}})
	case "tools/list":
		return ready(&reply{ID: m.ID, Result: map[string]any{"tools": s.h.Tools(&s.sess)}})
	case "tools/call":
		return s.call(ctx, m, wg)
	case "resources/list", "resources/read":
		if rh, ok := s.h.(ResourceHandler); ok {
			return s.resources(rh, m)
		}
	}
	return fail(m.ID, CodeMethodNotFound, "method not found: "+m.Method)
}

func (s *server) notify(m message) {
	if m.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	s.rmu.Lock()
	cancel := s.running[string(p.RequestID)]
	s.rmu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		Meta            struct {
			Run string `json:"fennec-cua/run"`
		} `json:"_meta"`
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &p)
	version := Versions[0]
	for _, v := range Versions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	s.sess.Version = version
	s.sess.Client = p.ClientInfo.Name
	s.sess.RunToken = p.Meta.Run
	s.sess.AgentID = agentID(s.info.IDPrefix, p.ClientInfo.Name, s.info.FixedID)
	instructions := s.info.Instructions
	if len(instructions) > 2048 {
		instructions = instructions[:2048]
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    s.capabilities(),
		"serverInfo":      map[string]any{"name": s.info.Name, "version": s.info.Version},
		"instructions":    instructions,
	}
}

func (s *server) call(ctx context.Context, m message, wg *sync.WaitGroup) *pending {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
		return fail(m.ID, CodeInvalidParams, "tools/call needs a tool name")
	}
	known := false
	for _, t := range s.h.Tools(&s.sess) {
		if t.Name == p.Name {
			known = true
			break
		}
	}
	if !known {
		return fail(m.ID, CodeInvalidParams, "unknown tool: "+p.Name)
	}
	if args := strings.TrimSpace(string(p.Arguments)); args != "" && args != "null" && !strings.HasPrefix(args, "{") {
		return fail(m.ID, CodeInvalidParams, "arguments must be an object")
	}
	cctx, cancel := context.WithCancel(ctx)
	key := string(m.ID)
	s.rmu.Lock()
	s.running[key] = cancel
	s.rmu.Unlock()
	pd := &pending{done: make(chan struct{})}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(pd.done)
		res := s.h.Call(cctx, &s.sess, p.Name, p.Arguments)
		s.rmu.Lock()
		delete(s.running, key)
		s.rmu.Unlock()
		cancelled := cctx.Err() != nil
		cancel()
		if cancelled {
			return // a cancelled request gets no reply
		}
		pd.reply = &reply{ID: m.ID, Result: content(res)}
	}()
	return pd
}

func content(r Result) map[string]any {
	blocks := []map[string]any{}
	if r.Text != "" || len(r.Images) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": r.Text})
	}
	for _, img := range r.Images {
		blocks = append(blocks, map[string]any{"type": "image", "data": img.Data, "mimeType": img.MIMEType})
	}
	out := map[string]any{"content": blocks, "isError": r.IsError}
	if r.Structured != nil {
		out["structuredContent"] = r.Structured
	}
	return out
}

// agentID builds prefix-<client>-<nonce>, or prefix-<fixed> when fixed is
// set. Characters other than ASCII letters and digits become '-', and each
// part is cut to 32 characters.
func agentID(prefix, client, fixed string) string {
	if prefix == "" {
		prefix = "mcp"
	}
	if fixed != "" {
		return prefix + "-" + clean(fixed)
	}
	if client == "" {
		client = "client"
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + clean(client) + "-" + hex.EncodeToString(b[:])
}

func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 32 {
			break
		}
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Resource describes one resource for resources/list.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ResourceContents is a resource read as text.
type ResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// ErrUnknownResource answers resources/read for a URI nobody serves.
var ErrUnknownResource = errors.New("unknown resource")

// ResourceHandler is optional: a Handler that also implements it serves
// resources/list and resources/read, and initialize names the resources
// capability while it lists any.
type ResourceHandler interface {
	Resources(s *Session) []Resource
	ReadResource(s *Session, uri string) (ResourceContents, error)
}

func (s *server) capabilities() map[string]any {
	caps := map[string]any{"tools": map[string]any{}}
	if rh, ok := s.h.(ResourceHandler); ok && len(rh.Resources(&s.sess)) > 0 {
		caps["resources"] = map[string]any{}
	}
	return caps
}

func (s *server) resources(rh ResourceHandler, m message) *pending {
	if m.Method == "resources/list" {
		list := rh.Resources(&s.sess)
		if list == nil {
			list = []Resource{}
		}
		return ready(&reply{ID: m.ID, Result: map[string]any{"resources": list}})
	}
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.URI == "" {
		return fail(m.ID, CodeInvalidParams, "resources/read needs a uri")
	}
	c, err := rh.ReadResource(&s.sess, p.URI)
	if errors.Is(err, ErrUnknownResource) {
		return fail(m.ID, CodeInvalidParams, "unknown resource: "+p.URI)
	}
	if err != nil {
		return fail(m.ID, CodeInternal, err.Error())
	}
	return ready(&reply{ID: m.ID, Result: map[string]any{"contents": []ResourceContents{c}}})
}
