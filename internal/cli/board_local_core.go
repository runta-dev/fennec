package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// errStaleCore: the core left running by an earlier Fennec.app refuses new
// terminals once the app (and its hc) was replaced ("no holder: holder build
// ... is not current for core build ..."). Restarting the core heals it.
var errStaleCore = errors.New("local terminal core is from an earlier Fennec version")

func (c *boardLocalCore) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, err := c.callLocked(ctx, method, params)
	// A refused terminal.run created nothing, so asking once more is safe.
	if errors.Is(err, errStaleCore) && method == "terminal.run" {
		log.Printf("queue: restarting the local terminal core after an update (%v)", err)
		if restartErr := c.restart(ctx); restartErr != nil {
			return nil, fmt.Errorf("%w; restart failed: %v", err, restartErr)
		}
		result, err = c.callLocked(ctx, method, params)
	}
	return result, err
}

func (c *boardLocalCore) command() (program, session string, err error) {
	program = c.program
	if program == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		program = filepath.Join(filepath.Dir(exe), "hc")
	}
	session = c.session
	if session == "" {
		session = "fennec"
	}
	return program, session, nil
}

// restart runs `hc server restart` for this core. Output goes nowhere: the
// restarted server can inherit pipes and keep a reader waiting.
func (c *boardLocalCore) restart(ctx context.Context) error {
	if c.process != nil {
		c.input.Close()
		c.process.Process.Kill()
		c.process = nil
	}
	program, session, err := c.command()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "server", "restart")
	cmd.Env = append(os.Environ(), "HC_SESSION="+session)
	return cmd.Run()
}

func (c *boardLocalCore) callLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.process == nil {
		program, session, err := c.command()
		if err != nil {
			return nil, err
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
		if ok && json.Unmarshal(raw, &reply) == nil && reply.ID == c.next && staleCoreError(reply.Error) {
			fail()
			return nil, fmt.Errorf("%w: %s", errStaleCore, reply.Error)
		}
		if !ok || json.Unmarshal(raw, &reply) != nil || reply.ID != c.next || len(reply.Result) == 0 || (len(reply.Error) > 0 && string(reply.Error) != "null") {
			fail()
			return nil, fmt.Errorf("local terminal operation was not confirmed")
		}
		return reply.Result, nil
	}
}

func staleCoreError(raw json.RawMessage) bool {
	text := string(raw)
	return strings.Contains(text, "no holder") && strings.Contains(text, "is not current")
}
