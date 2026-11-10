package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"golang.org/x/term"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTerminalChild(t *testing.T) {
	if os.Getenv("FENNEC_TERMINAL_TEST_CHILD") != "1" {
		return
	}
	if os.Getenv("FENNEC_TERMINAL_TEST_INPUT") == "1" {
		term.MakeRaw(int(os.Stdin.Fd()))
		os.Stdout.Write([]byte("READY"))
		data := make([]byte, 1)
		if _, err := os.Stdin.Read(data); err != nil {
			os.Exit(2)
		}
		if data[0] != 28 {
			os.Exit(3)
		}
		os.Stdout.Write([]byte("INPUT_OK"))
		os.Exit(0)
	}
	if os.Getenv("FENNEC_TERMINAL_TEST_NOISY") == "1" {
		os.Stdout.Write(bytes.Repeat([]byte("X"), Window*3))
	} else {
		os.Stdout.Write([]byte("OK"))
	}
	os.Exit(0)
}

func TestSlowTerminalCannotBlockAnotherSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer send.Close()
	defer receive.Close()
	noisy, quick := strings.Repeat("a", 32), strings.Repeat("b", 32)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, input, output, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			executable, err := os.Executable()
			if err != nil {
				return cloudruntime.LocalTerminal{}, nil, err
			}
			command := exec.Command(executable, "-test.run=^TestTerminalChild$")
			command.Env = append(os.Environ(), "FENNEC_TERMINAL_TEST_CHILD=1")
			if request.ID == noisy {
				command.Env = append(command.Env, "FENNEC_TERMINAL_TEST_NOISY=1")
			}
			return cloudruntime.LocalTerminal{ID: request.ID, Agent: request.Agent, Address: "t1", State: "live"}, command, nil
		})
	}()
	decoder := json.NewDecoder(receive)
	var frame Frame
	if err := decoder.Decode(&frame); err != nil || frame.Type != "ready" {
		t.Fatal(frame, err)
	}
	encoder := json.NewEncoder(send)
	for _, identity := range []string{noisy, quick} {
		request := cloudruntime.LocalLaunch{ID: identity, Agent: "codex", LocalDirectory: "/project", Device: "1234abcd"}
		if err := encoder.Encode(Frame{Type: "launch", ID: identity, Launch: &request, Rows: 40, Columns: 120}); err != nil {
			t.Fatal(err)
		}
	}
	noisyBytes := 0
	var quickOutput bytes.Buffer
	for {
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal("channel stopped before healthy terminal finished", err)
		}
		if frame.Type == "error" {
			t.Fatal(frame.Message)
		}
		if frame.Type == "data" {
			if frame.ID == noisy {
				noisyBytes += len(frame.Data)
			} else {
				quickOutput.Write(frame.Data)
			}
		}
		if frame.Type == "exit" && frame.ID == quick {
			if frame.Code != 0 {
				t.Fatal(frame.Code)
			}
			break
		}
	}
	if quickOutput.String() != "OK" || noisyBytes > Window {
		t.Fatal("output isolation/window failed", quickOutput.String(), noisyBytes)
	}
	cancel()
	send.Close()
	receive.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not release blocked terminal")
	}
}

func TestFlowWindowRejectsExcessCreditAndCancelsWait(t *testing.T) {
	window := newWindow()
	if window.add(1) {
		t.Fatal("accepted unsolicited credit")
	}
	if window.take(Window) != Window {
		t.Fatal("initial window unavailable")
	}
	done := make(chan int, 1)
	go func() { done <- window.take(1) }()
	window.close()
	if count := <-done; count != 0 {
		t.Fatal("closed window granted output", count)
	}
}

func TestHeartbeatPingDoesNotLaunchSession(t *testing.T) {
	var output bytes.Buffer
	err := Serve(context.Background(), strings.NewReader("{\"type\":\"ping\"}\n"), &output, func(context.Context, cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
		t.Fatal("heartbeat launched a session")
		return cloudruntime.LocalTerminal{}, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var ready, pong Frame
	if decoder.Decode(&ready) != nil || decoder.Decode(&pong) != nil || ready.Type != "ready" || pong.Type != "pong" {
		t.Fatal(output.String(), ready, pong)
	}
}

func TestFriendlySessionFrame(t *testing.T) {
	frame := Frame{Type: "attach", ID: "amber-cedar", Terminal: &cloudruntime.LocalTerminal{ID: "amber-cedar", Address: "t1"}}
	if err := frame.validateOpen(); err != nil {
		t.Fatal(err)
	}
}
