// Command bench-pages runs inside a cua-bench VM for the browser set
// (bench/sets/browser.md): it serves the task pages on 127.0.0.1 and logs
// what it sees, readies the VM for a task, snapshots Chromium when the
// agent stops, grades, and runs a task's scripted solution. cua-bench
// cross-builds it and pushes it into each clone.
//
//	bench-pages serve [-addr 127.0.0.1:8765] [-log /tmp/bench-pages]
//	bench-pages setup TASK      server, profile, files, Chromium on the start page
//	bench-pages snapshot        record Chromium's windows and tabs, then quit it
//	bench-pages grade TASK      print "true" or "false: why"
//	bench-pages solve TASK      the scripted solution (cua-bench check -solve)
//	bench-pages list            the tasks
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	browsersite "fennec/cua/bench/browser"
	"fennec/cua/internal/bench/browserset"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench-pages:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bench-pages serve | setup TASK | snapshot | grade TASK | solve TASK | list")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	task := func() (browserset.Def, error) {
		if len(args) != 2 {
			return browserset.Def{}, fmt.Errorf("usage: bench-pages %s TASK", args[0])
		}
		return browserset.Find(args[1])
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:])
	case "list":
		defs, err := browserset.Load()
		if err != nil {
			return err
		}
		for _, d := range defs {
			fmt.Printf("%s\t%s\t%s\n", d.ID, d.Family, d.Text(browserset.DefaultBase))
		}
		return nil
	case "setup":
		d, err := task()
		if err != nil {
			return err
		}
		env, err := browserset.GuestEnv()
		if err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		return browserset.Setup(ctx, env, d, self)
	case "snapshot":
		env, err := browserset.GuestEnv()
		if err != nil {
			return err
		}
		s, err := browserset.TakeSnapshot(env)
		if err != nil {
			return err
		}
		tabs := 0
		for _, w := range s.Windows {
			tabs += len(w.Tabs)
		}
		fmt.Printf("chromium running: %v, %d windows, %d tabs\n", s.Running, len(s.Windows), tabs)
		return nil
	case "grade":
		d, err := task()
		if err != nil {
			return err
		}
		env, err := browserset.GuestEnv()
		if err != nil {
			return err
		}
		r, err := browserset.Grade(env, d)
		if err != nil {
			return err
		}
		fmt.Println(r)
		return nil
	case "solve":
		d, err := task()
		if err != nil {
			return err
		}
		env, err := browserset.GuestEnv()
		if err != nil {
			return err
		}
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		return browserset.Solve(sctx, env, d, false, log.Printf)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func serve(ctx context.Context, args []string) error {
	fset := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fset.String("addr", browserset.DefaultAddr, "listen address (keep it on 127.0.0.1)")
	logDir := fset.String("log", browserset.GuestLogDir, "log folder")
	_ = fset.Parse(args)
	site, err := fs.Sub(browsersite.FS, "site")
	if err != nil {
		return err
	}
	l, err := browserset.OpenLog(*logDir)
	if err != nil {
		return err
	}
	defer l.Close()
	h, err := browserset.NewServer(site, l)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("serving on http://%s, logging to %s", ln.Addr(), *logDir)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
