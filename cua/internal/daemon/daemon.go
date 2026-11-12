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
	"time"

	"fennec/cua/internal/engine"
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
	// SocketGroup, when set (a group name or number), shares the agent
	// socket with that group: mode 0660 owned by the group, in a 0755
	// directory that passes CheckSharedDir. The control socket stays
	// 0600, and safety controls are answered only for the daemon's own
	// user.
	SocketGroup string
	// Ready, when set, is closed once both sockets listen.
	Ready chan struct{}
	// Starting, when set, runs once the daemon holds its lock and before
	// it listens, such as starting service plugins, which must never touch
	// another daemon's copies; an error fails the start.
	Starting func(ctx context.Context) error
	// Serving, when set, runs in its own goroutine once both sockets
	// listen, for slow setup such as building the model client, so the
	// first answer after a cold start never waits for it.
	Serving func(ctx context.Context)
}

// acceptor is a listener (tests fail its accepts).
type acceptor interface {
	AcceptUnix() (*net.UnixConn, error)
}

// Accept retry backoff after a failed accept, such as running out of file
// descriptors.
const (
	acceptRetryFirst = 5 * time.Millisecond
	acceptRetryMax   = time.Second
)

// Run serves until ctx ends.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Paths.Check(); err != nil {
		return err
	}
	gid, dirMode, sockMode := -1, PrivateDirMode, PrivateSocketMode
	if d.SocketGroup != "" {
		g, err := ResolveGroup(d.SocketGroup)
		if err != nil {
			return err
		}
		gid, dirMode, sockMode = g, SharedDirMode, SharedSocketMode
	}
	if err := endpointDir(d.Paths.Dir, dirMode); err != nil {
		return err
	}
	if gid >= 0 {
		if err := CheckSharedDir(d.Paths.Dir, uint32(os.Geteuid())); err != nil {
			return err
		}
	}
	lockFile, err := lock(d.Paths.Lock, d.Paths.MCP)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if d.Starting != nil {
		if err := d.Starting(ctx); err != nil {
			return err
		}
	}
	mcpLn, err := listen(d.Paths.MCP, sockMode, gid)
	if err != nil {
		return err
	}
	defer func() { _ = mcpLn.Close(); _ = os.Remove(d.Paths.MCP) }()
	ctlLn, err := listen(d.Paths.Control, PrivateSocketMode, -1)
	if err != nil {
		return err
	}
	defer func() { _ = ctlLn.Close(); _ = os.Remove(d.Paths.Control) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.Ready != nil {
		close(d.Ready)
	}
	var wg sync.WaitGroup
	if d.Serving != nil {
		wg.Add(1)
		go func() { defer wg.Done(); d.Serving(ctx) }()
	}
	go func() { <-ctx.Done(); _ = mcpLn.Close(); _ = ctlLn.Close() }()
	shared := gid >= 0
	wg.Add(2)
	go func() {
		defer wg.Done()
		d.accept(ctx, mcpLn, func(ctx context.Context, conn *net.UnixConn, peer uint32, own uint32) {
			if peer != own && !shared {
				d.logf("refused a connection from another user")
				return
			}
			// Group peers may call tools; the peer mark keeps recording
			// to the owner.
			d.serveMCP(engine.WithPeer(ctx, peer == own), conn)
		})
	}()
	go func() {
		defer wg.Done()
		d.accept(ctx, ctlLn, func(ctx context.Context, conn *net.UnixConn, peer uint32, own uint32) {
			d.serveControl(ctx, conn, peer, own)
		})
	}()
	wg.Wait()
	return nil
}

func (d *Daemon) accept(ctx context.Context, ln acceptor, serve func(ctx context.Context, conn *net.UnixConn, peer, own uint32)) {
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := time.Duration(0)
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			// A failed accept (out of descriptors, an aborted connection)
			// is retried after a growing pause instead of spinning.
			if backoff == 0 {
				backoff = acceptRetryFirst
				d.logf("accept: %v; retrying", err)
			} else if backoff *= 2; backoff > acceptRetryMax {
				backoff = acceptRetryMax
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		backoff = 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			peer, err := peerUID(conn)
			if err != nil {
				d.logf("refused a connection whose peer is unknown: %v", err)
				return
			}
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			serve(cctx, conn, peer, ownUID())
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

func (d *Daemon) serveControl(_ context.Context, conn *net.UnixConn, peer, own uint32) {
	sc := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req ControlRequest
		var rep ControlReply
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			rep.Error = "bad request: " + err.Error()
		} else {
			rep = d.control(peer, own, req)
		}
		if err := enc.Encode(rep); err != nil {
			return
		}
	}
}

// control answers one control request from a peer running as uid peer;
// every control is the daemon's own user's alone.
func (d *Daemon) control(peer, own uint32, req ControlRequest) ControlReply {
	if err := OwnerOnly(peer, own); err != nil {
		return ControlReply{Error: req.Op + ": " + err.Error()}
	}
	switch req.Op {
	case "stop":
		d.Stopper.Stop()
		return ControlReply{OK: true}
	case "resume":
		d.Stopper.Resume()
		return ControlReply{OK: true}
	case "status":
		return ControlReply{OK: true, Status: d.Handler.Status()}
	}
	return ControlReply{Error: "unknown op " + req.Op}
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
