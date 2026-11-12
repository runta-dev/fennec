// Package mcpclient is a minimal MCP client over a child process's stdio. The
// comparison harness uses it to drive fennec-cua and shuttle the same way an
// agent does.
package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
)

// Client is one MCP session.
type Client struct {
	w       io.WriteCloser
	mu      sync.Mutex // serialises writes
	pending map[int64]chan response
	pmu     sync.Mutex
	next    int64
	done    chan struct{}
	err     error
	cmd     *exec.Cmd
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ToolResult is a tools/call result.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError"`
	// Structured is the structuredContent, when the server sends one.
	Structured json.RawMessage `json:"structuredContent,omitempty"`
}

// Content is one content block.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Text joins the text blocks.
func (r ToolResult) Text() string {
	s := ""
	for _, c := range r.Content {
		if c.Type == "text" {
			s += c.Text
		}
	}
	return s
}

// Start runs the command and initialises a session.
func Start(ctx context.Context, name string, args ...string) (*Client, error) {
	cmd := exec.Command(name, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := New(in, out)
	c.cmd = cmd
	if err := c.Initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Dial opens a session on a unix socket that speaks MCP directly, such as
// the fennec-cua daemon's.
func Dial(ctx context.Context, socket string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := New(conn, conn)
	if err := c.Initialize(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// New wraps an existing transport; call Initialize before tools.
func New(w io.WriteCloser, r io.Reader) *Client {
	c := &Client{w: w, pending: map[int64]chan response{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

func (c *Client) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var resp response
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil || resp.ID == nil {
			continue // notifications and noise
		}
		c.pmu.Lock()
		ch := c.pending[*resp.ID]
		delete(c.pending, *resp.ID)
		c.pmu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	c.err = sc.Err()
	if c.err == nil {
		c.err = io.EOF
	}
	close(c.done)
}

func (c *Client) send(r request) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// Call sends a request and waits for its response. Cancelling ctx sends
// notifications/cancelled for it.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	c.pmu.Lock()
	c.next++
	id := c.next
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.pmu.Unlock()
	if err := c.send(request{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("%s: %d %s", method, resp.Error.Code, resp.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	case <-ctx.Done():
		_ = c.send(request{JSONRPC: "2.0", Method: "notifications/cancelled", Params: map[string]any{"requestId": id, "reason": ctx.Err().Error()}})
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("%s: connection closed: %w", method, c.err)
	}
}

// Initialize performs the MCP handshake.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "fennec-cua-parity", "version": "0"},
	}
	if err := c.Call(ctx, "initialize", params, nil); err != nil {
		return err
	}
	return c.send(request{JSONRPC: "2.0", Method: "notifications/initialized"})
}

// Tool calls a tool.
func (c *Client) Tool(ctx context.Context, name string, args any) (ToolResult, error) {
	var r ToolResult
	err := c.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &r)
	return r, err
}

// Tools lists tool names.
func (c *Client) Tools(ctx context.Context) ([]string, error) {
	var r struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := c.Call(ctx, "tools/list", map[string]any{}, &r); err != nil {
		return nil, err
	}
	names := make([]string, len(r.Tools))
	for i, t := range r.Tools {
		names[i] = t.Name
	}
	return names, nil
}

// Close ends the session and the child process.
func (c *Client) Close() error {
	err := c.w.Close()
	if c.cmd != nil {
		if werr := c.cmd.Wait(); werr != nil && !errors.Is(werr, io.EOF) && err == nil {
			err = werr
		}
	}
	return err
}
