//go:build unix

// Package stackdump writes every goroutine's stack to a private file on
// SIGUSR1 without stopping the process. It exists for hangs that log nothing,
// such as a stream that silently stops delivering bytes.
package stackdump

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Install handles SIGUSR1 until ctx ends. Each signal writes
// dir/goroutines-<pid>-<UTC time>.txt (0600). Install before anything can
// receive the signal: Go's default action for SIGUSR1 terminates the process.
func Install(ctx context.Context, dir string) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	go func() {
		defer signal.Stop(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				_, _ = Write(dir)
			}
		}
	}()
}
