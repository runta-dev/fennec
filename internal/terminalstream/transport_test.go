package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTransportServiceChild(t *testing.T) {
	if os.Getenv("FENNEC_TRANSPORT_TEST_SERVICE") != "1" {
		return
	}
	if os.Getenv("FENNEC_DELAY_READY") == "1" {
		gate := os.NewFile(3, "ready-gate")
		var b [1]byte
		if _, err := gate.Read(b[:]); err != nil {
			os.Exit(2)
		}
		gate.Close()
	}
	var input io.Reader = os.Stdin
	if id := os.Getenv("FENNEC_INITIAL_TEST_ID"); id != "" {
		frame := Frame{Type: "launch", ID: id, Launch: &cloudruntime.LocalLaunch{ID: id, Agent: "codex", Device: "1234abcd", LocalDirectory: "/project"}}
		raw, _ := json.Marshal(frame)
		input = io.MultiReader(bytes.NewReader(append(raw, '\n')), os.Stdin)
	}
	err := Serve(context.Background(), input, os.Stdout, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
		executable, err := os.Executable()
		if err != nil {
			return cloudruntime.LocalTerminal{}, nil, err
		}
		command := exec.Command(executable, "-test.run=^TestTerminalChild$")
		command.Env = append(os.Environ(), "FENNEC_TERMINAL_TEST_CHILD=1")
		if len(request.Arguments) > 0 && request.Arguments[0] == "input-test" {
			command.Env = append(command.Env, "FENNEC_TERMINAL_TEST_INPUT=1")
		}
		return cloudruntime.LocalTerminal{ID: request.ID, Agent: request.Agent, Address: "t1", State: "live"}, command, nil
	}, func(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
		executable, err := os.Executable()
		if err != nil {
			return reference, nil, err
		}
		command := exec.Command(executable, "-test.run=^TestTerminalChild$")
		command.Env = append(os.Environ(), "FENNEC_TERMINAL_TEST_CHILD=1")
		reference.Agent = "codex"
		reference.State = "live"
		return reference, command, nil
	})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestTransportForwardsTerminalControlInput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportServiceChild$")
	command.Env = append(os.Environ(), "FENNEC_TRANSPORT_TEST_SERVICE=1")
	transport, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("d", 32)
	request := cloudruntime.LocalLaunch{ID: id, Agent: "codex", LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"input-test"}}
	session, err := transport.Launch(Frame{Type: "launch", ID: id, Launch: &request, Rows: 40, Columns: 120})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var output bytes.Buffer
	sent := false
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == "data" {
			output.Write(frame.Data)
			session.Send(Frame{Type: "credit", Bytes: len(frame.Data)})
			if !sent && bytes.Contains(output.Bytes(), []byte("READY")) {
				sent = true
				session.Send(Frame{Type: "input", Data: []byte{28}})
			}
		}
		if frame.Type == "exit" {
			if frame.Code != 0 || !bytes.Contains(output.Bytes(), []byte("INPUT_OK")) {
				t.Fatal(frame.Code, output.String())
			}
			return
		}
	}
}

func TestTransportReusesOneProcessForDistinctSessions(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportServiceChild$")
	command.Env = append(os.Environ(), "FENNEC_TRANSPORT_TEST_SERVICE=1")
	transport, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for _, letter := range []string{"a", "b", "c"} {
		id := strings.Repeat(letter, 32)
		request := cloudruntime.LocalLaunch{ID: id, Agent: "codex", LocalDirectory: "/project", Device: "1234abcd"}
		opening := Frame{Type: "launch", ID: id, Launch: &request, Rows: 40, Columns: 120}
		terminalID := id
		if letter == "c" {
			terminalID = strings.Repeat("a", 32)
			opening = Frame{Type: "attach", ID: id, Terminal: &cloudruntime.LocalTerminal{ID: terminalID, Address: "t1"}, Rows: 40, Columns: 120}
		}
		session, err := transport.Launch(opening)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.Launch(Frame{Type: "launch", ID: id, Launch: &request}); err == nil {
			t.Fatal("duplicate stream accepted")
		}
		var output bytes.Buffer
		receipt := false
		for {
			frame, err := session.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ID != id {
				t.Fatal("cross-session frame")
			}
			switch frame.Type {
			case "receipt":
				receipt = frame.Terminal != nil && frame.Terminal.ID == terminalID
			case "data":
				output.Write(frame.Data)
				session.Send(Frame{Type: "credit", Bytes: len(frame.Data)})
			case "error":
				t.Fatal(frame.Message)
			}
			if frame.Type == "exit" {
				if frame.Code != 0 {
					t.Fatal(frame.Code)
				}
				break
			}
		}
		if !receipt || output.String() != "OK" || !transport.Alive() {
			t.Fatal("terminal exchange failed", receipt, output.String())
		}
		session.Close()
		session.Close()
	}
}

func TestInitialLaunchReceivesEarlyFramesWithoutResending(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := strings.Repeat("b", 32)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportServiceChild$")
	command.Env = append(os.Environ(), "FENNEC_TRANSPORT_TEST_SERVICE=1", "FENNEC_INITIAL_TEST_ID="+id)
	opening := Frame{Type: "launch", ID: id, Launch: &cloudruntime.LocalLaunch{ID: id, Agent: "codex", Device: "1234abcd", LocalDirectory: "/project"}}
	transport, session, err := StartInitial(command, opening)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	defer session.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	receipts := 0
	var output bytes.Buffer
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch frame.Type {
		case "receipt":
			receipts++
		case "data":
			output.Write(frame.Data)
		case "error":
			t.Fatal(frame.Message)
		case "exit":
			if receipts != 1 || output.String() != "OK" || frame.Code != 0 {
				t.Fatal(receipts, output.String(), frame.Code)
			}
			return
		}
	}
}

func TestLaunchCanQueueBeforeReadyWithoutReplay(t *testing.T) {
	executable, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gate, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	defer release.Close()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportServiceChild$")
	command.Env = append(os.Environ(), "FENNEC_TRANSPORT_TEST_SERVICE=1", "FENNEC_DELAY_READY=1")
	command.ExtraFiles = []*os.File{gate}
	transport, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	gate.Close()
	if transport.IsReady() {
		t.Fatal("transport claimed ready before server greeting")
	}
	id := strings.Repeat("e", 32)
	session, err := transport.Launch(Frame{Type: "launch", ID: id, Launch: &cloudruntime.LocalLaunch{ID: id, Agent: "codex", LocalDirectory: "/project", Device: "1234abcd"}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	release.Write([]byte{1})
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.IsReady() {
		t.Fatal("validated greeting not reflected")
	}
	receipts := 0
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == "receipt" {
			receipts++
		}
		if frame.Type == "error" {
			t.Fatal(frame.Message)
		}
		if frame.Type == "exit" {
			if receipts != 1 {
				t.Fatal("initial launch replayed", receipts)
			}
			return
		}
	}
}
