package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"sync"

	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/sched"
)

// Handler is what the daemon serves to agents; Status feeds the control
// socket.
type Handler interface {
	mcpserver.Handler
	Status() any
}

// Daemon is one per user.
type Daemon struct {
	Paths   Paths
	Handler Handler
	Stopper *sched.Stopper
	Info    mcpserver.Info
	Log     *log.Logger
	// Ready, when set, is closed once both sockets listen.
	Ready chan struct{}
}

// Run serves until ctx ends.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Paths.Check(); err != nil {
		return err
	}
	if err := os.MkdirAll(d.Paths.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(d.Paths.Dir, 0o700); err != nil {
		return err
	}
	lockFile, err := lock(d.Paths.Lock)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	mcpLn, err := listen(d.Paths.MCP, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = mcpLn.Close(); _ = os.Remove(d.Paths.MCP) }()
	ctlLn, err := listen(d.Paths.Control, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = ctlLn.Close(); _ = os.Remove(d.Paths.Control) }()
	if d.Ready != nil {
		close(d.Ready)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); _ = mcpLn.Close(); _ = ctlLn.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.accept(ctx, mcpLn, d.serveMCP) }()
	go func() { defer wg.Done(); d.accept(ctx, ctlLn, d.serveControl) }()
	wg.Wait()
	return nil
}

func (d *Daemon) accept(ctx context.Context, ln *net.UnixListener, serve func(context.Context, *net.UnixConn)) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			d.logf("accept: %v", err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			if !sameUser(conn) {
				d.logf("refused a connection from another user")
				return
			}
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			serve(cctx, conn)
		}()
	}
}

// serveMCP runs one agent session. When the client drops the connection,
// Serve sees EOF and cancels the session's calls.
func (d *Daemon) serveMCP(ctx context.Context, conn *net.UnixConn) {
	if err := mcpserver.Serve(ctx, conn, conn, d.Handler, d.Info); err != nil && ctx.Err() == nil {
		d.logf("session: %v", err)
	}
}

// ControlRequest is one line on the control socket.
type ControlRequest struct {
	Op string `json:"op"` // stop, resume, status
}

// ControlReply answers it.
type ControlReply struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Status any    `json:"status,omitempty"`
}

func (d *Daemon) serveControl(_ context.Context, conn *net.UnixConn) {
	sc := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req ControlRequest
		var rep ControlReply
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			rep.Error = "bad request: " + err.Error()
		} else {
			switch req.Op {
			case "stop":
				d.Stopper.Stop()
				rep.OK = true
			case "resume":
				d.Stopper.Resume()
				rep.OK = true
			case "status":
				rep.OK, rep.Status = true, d.Handler.Status()
			default:
				rep.Error = "unknown op " + req.Op
			}
		}
		if err := enc.Encode(rep); err != nil {
			return
		}
	}
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log.Printf(format, args...)
	}
}

// Control sends one control request to a running daemon.
func Control(ctx context.Context, p Paths, op string) (ControlReply, error) {
	var dl net.Dialer
	conn, err := dl.DialContext(ctx, "unix", p.Control)
	if err != nil {
		return ControlReply{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(ControlRequest{Op: op}); err != nil {
		return ControlReply{}, err
	}
	var rep ControlReply
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		return ControlReply{}, err
	}
	if !rep.OK {
		return rep, errors.New(rep.Error)
	}
	return rep, nil
}
