package runtime

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A watcher's first paint arrives at once; later output is the program drawing.
const (
	observeQuiet = 40 * time.Millisecond
	observeMost  = 400 * time.Millisecond
	observeLimit = 1 << 20
)

// herdrObserve watches a terminal without typing or resizing it and returns
// the paint herdr-core sends a new watcher: the screen as escape sequences.
func herdrObserve(home string) func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, address string) ([]byte, error) {
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		command := exec.CommandContext(step, filepath.Join(home, ".local/bin/herdr-core"), "attach", "--observe", address)
		// Inside a herdr terminal the core would see a watcher of itself.
		command.Env = withoutHerdrTerminal(os.Environ())
		// A piped watcher ends when its input does: keep it open until we stop.
		input, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		defer input.Close()
		output, err := command.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := command.Start(); err != nil {
			return nil, err
		}
		defer func() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}()
		return collectPaint(step, output, observeQuiet, observeMost)
	}
}

func withoutHerdrTerminal(env []string) []string {
	kept := env[:0:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, "HC_TERMINAL_ID=") {
			kept = append(kept, entry)
		}
	}
	return kept
}

// collectPaint reads until output pauses for quiet after its first bytes, or
// until most has passed since then.
func collectPaint(ctx context.Context, output io.Reader, quiet, most time.Duration) ([]byte, error) {
	chunks := make(chan []byte)
	failed := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64<<10)
		for {
			n, err := output.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case chunks <- chunk:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				failed <- err
				return
			}
		}
	}()
	var paint []byte
	var deadline <-chan time.Time
	var pause *time.Timer
	for {
		var paused <-chan time.Time
		if pause != nil {
			paused = pause.C
		}
		select {
		case chunk := <-chunks:
			if paint == nil {
				deadline = time.After(most)
			}
			paint = append(paint, chunk...)
			if len(paint) > observeLimit {
				return paint[:observeLimit], nil
			}
			if pause == nil {
				pause = time.NewTimer(quiet)
			} else {
				pause.Reset(quiet)
			}
		case <-paused:
			return paint, nil
		case <-deadline:
			return paint, nil
		case err := <-failed:
			if len(paint) > 0 {
				return paint, nil
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		case <-ctx.Done():
			if len(paint) > 0 {
				return paint, nil
			}
			return nil, ctx.Err()
		}
	}
}
