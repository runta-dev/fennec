// Command fennec-cua is Fennec's computer-use host for macOS.
//
//	fennec-cua mcp       MCP on stdio, bridged to the daemon (started if needed)
//	fennec-cua serve     run the daemon in the foreground
//	fennec-cua stop      stop every call until resume
//	fennec-cua resume    lift a stop
//	fennec-cua status    print the daemon's status
//	fennec-cua version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"fennec/cua/internal/daemon"
	"fennec/cua/internal/desk"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/macos"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/view"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fennec-cua:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: fennec-cua mcp | serve | stop | resume | status | version")
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "version":
		fmt.Println(engine.Version)
		return nil
	case "serve":
		return serve(ctx, paths)
	case "mcp":
		return bridge(ctx, paths)
	case "stop", "resume", "status":
		cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
		defer ccancel()
		rep, err := daemon.Control(cctx, paths, args[0])
		if err != nil {
			return fmt.Errorf("%s: %w (is the daemon running?)", args[0], err)
		}
		if rep.Status != nil {
			out, _ := json.MarshalIndent(rep.Status, "", "  ")
			fmt.Println(string(out))
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

type platform struct{}

func (platform) AccessibilityTrusted() bool             { return macos.AccessibilityTrusted() }
func (platform) ScreenCaptureAllowed() bool             { return macos.ScreenCaptureAllowed() }
func (platform) MainDisplayPixels() (w, h int, ok bool) { return macos.MainDisplayPixels() }

func serve(ctx context.Context, paths daemon.Paths) error {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger := log.New(io.MultiWriter(logFile, os.Stderr), "fennec-cua ", log.LstdFlags|log.Lmicroseconds)

	stopper := &sched.Stopper{}
	input := "foreground"
	observer := &view.Observer{Source: desk.New(), Refs: view.NewRegistry(view.MaxRefs), Input: input}
	eng := &engine.Engine{
		Turn:     &sched.Turn{},
		Stopper:  stopper,
		Platform: platform{},
		Input:    input,
		Impl:     map[string]engine.Tool{protocol.ToolObserve: engine.ObserveTool(observer)},
	}
	if bin := driverBin(); bin != "" {
		sup := &driver.Supervisor{Bin: bin, Socket: paths.Driver, Log: logger}
		dctx, dcancel := context.WithCancel(ctx)
		defer func() { dcancel(); sup.Wait() }()
		go sup.Run(dctx)
	} else {
		logger.Printf("no bundled cua-driver; desktop tools are unavailable")
	}
	d := &daemon.Daemon{
		Paths:   paths,
		Handler: eng,
		Stopper: stopper,
		Info:    mcpserver.Info{Name: "fennec-cua", Version: engine.Version, Instructions: engine.Instructions, IDPrefix: "mcp"},
		Log:     logger,
	}
	logger.Printf("serving %s", paths.MCP)
	return d.Run(ctx)
}

// driverBin finds cua-driver: FENNEC_CUA_DRIVER, else beside this binary
// (an app bundle's Helpers directory).
func driverBin() string {
	if p := os.Getenv("FENNEC_CUA_DRIVER"); p != "" {
		return p
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(self), "cua-driver")
	if fi, err := os.Stat(p); err == nil && fi.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// bridge connects stdio to the daemon's MCP socket, starting the daemon when
// nothing listens. The daemon parses MCP itself; the bridge only copies.
func bridge(ctx context.Context, paths daemon.Paths) error {
	conn, err := dialOrStart(ctx, paths)
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, conn)
		close(done)
	}()
	_, _ = io.Copy(conn, os.Stdin)
	_ = conn.CloseWrite()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

func dialOrStart(ctx context.Context, paths daemon.Paths) (*net.UnixConn, error) {
	dial := func() (*net.UnixConn, error) {
		return net.DialUnix("unix", nil, &net.UnixAddr{Name: paths.MCP, Net: "unix"})
	}
	if conn, err := dial(); err == nil {
		return conn, nil
	}
	if os.Getenv("FENNEC_CUA_EMBEDDED") == "1" {
		return nil, errors.New("the daemon is not running and FENNEC_CUA_EMBEDDED=1 forbids starting it")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The daemon must hold none of the bridge's stdio, or the agent would
	// wait on it after the session ends.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := dial(); err == nil {
			return conn, nil
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("the daemon did not start; see %s", paths.Log)
}
