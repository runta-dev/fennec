package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Model an attachment that handles HUP but cannot finish until its blocked
// terminal write returns. The real attachment also restores terminal state
// while unwinding, so signaling it without closing the PTY is insufficient.
func TestBlockedAttachmentChild(t *testing.T) {
	mode := os.Getenv("FENNEC_BLOCKED_ATTACHMENT")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGHUP)
	os.Stdout.Write(bytes.Repeat([]byte("x"), Window*4))
	if mode == "stubborn" {
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}

func TestServerReapsBlockedAttachment(t *testing.T) {
	for _, mode := range []string{"blocked", "stubborn"} {
		for _, ending := range []string{"eof", "close", "lease"} {
			t.Run(mode+"/"+ending, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				input, send := io.Pipe()
				receive, output := io.Pipe()
				defer send.Close()
				defer receive.Close()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				command := exec.Command(executable, "-test.run=^TestBlockedAttachmentChild$")
				command.Env = append(os.Environ(), "FENNEC_BLOCKED_ATTACHMENT="+mode)
				defer func() {
					if command.Process != nil {
						command.Process.Kill()
					}
				}()
				timeout := time.Minute
				if ending == "lease" {
					timeout = 500 * time.Millisecond
				}
				done := make(chan error, 1)
				go func() {
					done <- serveWithIdleTimeout(ctx, input, output, nil, nil, nil, nil, timeout, func(_ context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
						return reference, command, nil
					})
				}()
				decoder := json.NewDecoder(receive)
				var frame Frame
				if err := decoder.Decode(&frame); err != nil || frame.Type != "ready" {
					t.Fatal(frame, err)
				}
				encoder := json.NewEncoder(send)
				id := "blocked-attachment"
				if err := encoder.Encode(Frame{Type: "attach", ID: id, Terminal: &cloudruntime.LocalTerminal{ID: id, Address: "t1"}}); err != nil {
					t.Fatal(err)
				}
				// Exhaust the output window without returning any credits.
				for received := 0; received < Window; {
					if err := decoder.Decode(&frame); err != nil {
						t.Fatal(err)
					}
					if frame.Type == "error" {
						t.Fatal(frame.Message)
					}
					if frame.Type == "data" {
						received += len(frame.Data)
					}
				}
				if ending == "close" {
					if err := encoder.Encode(Frame{Type: "close", ID: id}); err != nil {
						t.Fatal(err)
					}
					// Keep consuming the shared channel so the exit can be sent.
					exited := make(chan error, 1)
					go func() {
						for {
							if err := decoder.Decode(&frame); err != nil {
								exited <- err
								return
							}
							if frame.Type == "exit" {
								exited <- nil
								return
							}
						}
					}()
					select {
					case err := <-exited:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("closing one stream did not reap its attachment")
					}
					// Reaping one attachment must preserve the shared connection.
					if err := encoder.Encode(Frame{Type: "ping", ID: "still-live"}); err != nil {
						t.Fatal(err)
					}
					if err := decoder.Decode(&frame); err != nil || frame.Type != "pong" {
						t.Fatal(frame, err)
					}
				}
				if ending != "lease" {
					send.Close()
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("server retained a blocked attachment after disconnection")
				}
				if command.ProcessState == nil {
					t.Fatal("attachment was not reaped")
				}
			})
		}
	}
}

func TestServerLeaseRequiresClientTraffic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer send.Close()
	defer receive.Close()
	done := make(chan error, 1)
	go func() { done <- serveWithIdleTimeout(ctx, input, output, nil, nil, nil, nil, 200*time.Millisecond) }()
	decoder, encoder := json.NewDecoder(receive), json.NewEncoder(send)
	var frame Frame
	if err := decoder.Decode(&frame); err != nil || frame.Type != "ready" {
		t.Fatal(frame, err)
	}
	for range 12 {
		time.Sleep(40 * time.Millisecond)
		if err := encoder.Encode(Frame{Type: "ping"}); err != nil {
			t.Fatal("healthy idle connection expired", err)
		}
		if err := decoder.Decode(&frame); err != nil || frame.Type != "pong" {
			t.Fatal(frame, err)
		}
	}
	// A half-open connection never delivers EOF. Stop heartbeats but leave
	// both pipes open; the server must still exit on its own.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("half-open connection did not expire")
	}
}

func TestServerLeaseInterruptsBlockedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer send.Close()
	defer receive.Close()
	done := make(chan error, 1)
	go func() { done <- serveWithIdleTimeout(ctx, input, output, nil, nil, nil, nil, 100*time.Millisecond) }()
	// Do not even read the initial ready frame.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lease could not interrupt a blocked protocol write")
	}
}
