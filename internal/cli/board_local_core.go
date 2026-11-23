package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// One resident local-core connection per dispatcher; never rerun a mutation
// after an uncertain reply. It uses the same isolated core as Desktop tabs.
type boardLocalCore struct {
	mu       sync.Mutex
	lifetime context.Context
	process  *exec.Cmd
	input    io.WriteCloser
	replies  chan json.RawMessage
	next     uint64
	program  string
	session  string
}

func (c *boardLocalCore) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process == nil {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		program := c.program
		if program == "" {
			program = filepath.Join(filepath.Dir(exe), "hc")
		}
		session := c.session
		if session == "" {
			session = "fennec"
		}
		cmd := exec.CommandContext(c.lifetime, program, "connect")
		cmd.Env = append(os.Environ(), "HC_SESSION="+session)
		input, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err = cmd.Start(); err != nil {
			return nil, err
		}
		c.process, c.input, c.replies = cmd, input, make(chan json.RawMessage, 1)
		replies := c.replies
		go func() {
			defer close(replies)
			defer cmd.Wait()
			scan := bufio.NewScanner(output)
			scan.Buffer(make([]byte, 4096), 4<<20)
			for scan.Scan() {
				line := append(json.RawMessage(nil), scan.Bytes()...)
				select {
				case replies <- line:
				case <-c.lifetime.Done():
					return
				}
			}
		}()
	}
	fail := func() { c.input.Close(); c.process.Process.Kill(); c.process = nil }
	c.next++
	if err := json.NewEncoder(c.input).Encode(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params}); err != nil {
		fail()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		fail()
		return nil, ctx.Err()
	case raw, ok := <-c.replies:
		var reply struct {
			ID     uint64          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if !ok || json.Unmarshal(raw, &reply) != nil || reply.ID != c.next || len(reply.Result) == 0 || (len(reply.Error) > 0 && string(reply.Error) != "null") {
			fail()
			return nil, fmt.Errorf("local terminal operation was not confirmed")
		}
		return reply.Result, nil
	}
}
