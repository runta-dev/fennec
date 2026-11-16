package main

import (
	"bytes"
	"encoding/json"
	"fennec/internal/terminalstream"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStreamServiceCommandChild(t *testing.T) {
	if os.Getenv("FENNEC_STREAM_SERVICE_CHILD") != "1" {
		return
	}
	os.Args = []string{"fennec-terminal", "serve", "--protocol=" + strconv.Itoa(terminalstream.ProtocolVersion)}
	code, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func TestStreamServiceCancelsInheritedPipes(t *testing.T) {
	for _, blocked := range []string{"input", "output"} {
		t.Run(blocked, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(binary, "-test.run=^TestStreamServiceCommandChild$")
			// Native macOS test temp paths exceed the Unix socket path limit.
			home, err := os.MkdirTemp("/tmp", "fn-stream-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			command.Env = append(os.Environ(), "FENNEC_STREAM_SERVICE_CHILD=1", "HOME="+home)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			var ready terminalstream.Frame
			if err := json.NewDecoder(output).Decode(&ready); err != nil || ready.Type != "ready" {
				command.Process.Kill()
				command.Wait()
				t.Fatal(ready, err, stderr.String())
			}
			if blocked == "output" {
				// Its pong cannot fit in the inherited stdout pipe. Leave the
				// reader open, but do not drain it while shutting down.
				if err := json.NewEncoder(input).Encode(terminalstream.Frame{Type: "ping", ID: strings.Repeat("x", 200000)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation left the service stuck in inherited " + blocked)
			}
		})
	}
}
