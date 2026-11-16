package terminalstream

import (
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"golang.org/x/term"
)

// TestEchoChild echoes every byte back, or swallows input when asked to model
// a busy agent.
func TestEchoChild(t *testing.T) {
	if os.Getenv("FENNEC_ECHO_CHILD") == "" {
		return
	}
	term.MakeRaw(int(os.Stdin.Fd()))
	os.Stdout.Write([]byte("READY"))
	buffer := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buffer)
		if err != nil {
			os.Exit(0)
		}
		if os.Getenv("FENNEC_ECHO_CHILD") == "echo" {
			os.Stdout.Write(buffer[:n])
		}
	}
}

func TestEchoAckerQuietAndBusy(t *testing.T) {
	a := newEchoAcker()
	start := time.Now()
	a.input(7)
	if _, ok := a.due(start); ok {
		t.Fatal("acknowledged before any output or timeout")
	}
	a.observe()
	if _, ok := a.due(time.Now()); ok {
		t.Fatal("acknowledged before output went quiet")
	}
	frame, ok := a.due(time.Now().Add(echoQuiet))
	if !ok || frame.Seq != 7 || frame.Code != 0 {
		t.Fatalf("quiet ack = %+v %v", frame, ok)
	}
	a.input(9)
	frame, ok = a.due(time.Now().Add(echoTimeout))
	if !ok || frame.Seq != 9 || frame.Code != 1 {
		t.Fatalf("busy ack = %+v %v", frame, ok)
	}
	var none *echoAcker
	none.input(1)
	none.observe()
}

func TestCadencePacesOnlyBunchedKeys(t *testing.T) {
	var c cadence
	now := time.Now()
	if c.wait(now, 30*time.Millisecond) != 0 {
		t.Fatal("first key must not wait")
	}
	c.wrote(now)
	if got := c.wait(now.Add(5*time.Millisecond), 30*time.Millisecond); got != 25*time.Millisecond {
		t.Fatalf("bunched key waits %v, want 25ms", got)
	}
	c.wrote(now.Add(30 * time.Millisecond))
	if got := c.wait(now.Add(130*time.Millisecond), 30*time.Millisecond); got != 0 {
		t.Fatalf("key typed after a pause waits %v", got)
	}
	c.wrote(now.Add(130 * time.Millisecond))
	if got := c.wait(now.Add(131*time.Millisecond), 500*time.Millisecond); got != cadenceMax-time.Millisecond {
		t.Fatalf("long gaps are capped, got %v", got)
	}
	// The budget bounds how long one backlog is paced.
	c = cadence{spent: cadenceBudget}
	c.wrote(now)
	if got := c.wait(now.Add(time.Millisecond), 30*time.Millisecond); got != 0 {
		t.Fatalf("exhausted budget still waits %v", got)
	}
}

type echoSession struct {
	t       *testing.T
	decoder *json.Decoder
	encoder *json.Encoder
	cancel  context.CancelFunc
	id      string
}

func startEchoSession(t *testing.T, mode string, features []string) (*echoSession, Frame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	input, send := io.Pipe()
	receive, output := io.Pipe()
	t.Cleanup(func() { cancel(); send.Close(); receive.Close() })
	go Serve(ctx, input, output, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
		executable, err := os.Executable()
		if err != nil {
			return cloudruntime.LocalTerminal{}, nil, err
		}
		command := exec.Command(executable, "-test.run=^TestEchoChild$")
		command.Env = append(os.Environ(), "FENNEC_ECHO_CHILD="+mode)
		return cloudruntime.LocalTerminal{ID: request.ID, Agent: request.Agent, Address: "t1", State: "live"}, command, nil
	})
	s := &echoSession{t: t, decoder: json.NewDecoder(receive), encoder: json.NewEncoder(send), cancel: cancel, id: "echo-alpha"}
	var frame Frame
	if err := s.decoder.Decode(&frame); err != nil || frame.Type != "ready" {
		t.Fatal(frame, err)
	}
	request := cloudruntime.LocalLaunch{ID: s.id, Agent: "claude", LocalDirectory: "/project", Device: "1234abcd"}
	if err := s.encoder.Encode(Frame{Type: "launch", ID: s.id, Launch: &request, Rows: 24, Columns: 80, Features: features}); err != nil {
		t.Fatal(err)
	}
	var receipt Frame
	for {
		frame := s.next()
		if frame.Type == "receipt" {
			receipt = frame
		}
		if frame.Type == "data" && string(frame.Data) == "READY" {
			s.send(Frame{Type: "credit", Bytes: len(frame.Data)})
			return s, receipt
		}
	}
}

func (s *echoSession) next() Frame {
	s.t.Helper()
	var frame Frame
	if err := s.decoder.Decode(&frame); err != nil {
		s.t.Fatal(err)
	}
	if err := expandTerminalData(&frame); err != nil {
		s.t.Fatal(err)
	}
	return frame
}

func (s *echoSession) send(frame Frame) {
	s.t.Helper()
	frame.ID = s.id
	if err := s.encoder.Encode(frame); err != nil {
		s.t.Fatal(err)
	}
}

func TestEchoAckFollowsAgentOutput(t *testing.T) {
	s, receipt := startEchoSession(t, "echo", []string{CapabilityEchoAck, CapabilityInputCadence})
	if !slices.Contains(receipt.Capabilities, CapabilityEchoAck) || !slices.Contains(receipt.Capabilities, CapabilityInputCadence) {
		t.Fatalf("receipt capabilities %v", receipt.Capabilities)
	}
	s.send(Frame{Type: "input", Data: []byte("hi"), Seq: 2})
	sawEcho := false
	for {
		frame := s.next()
		switch frame.Type {
		case "data":
			sawEcho = sawEcho || string(frame.Data) == "hi"
			s.send(Frame{Type: "credit", Bytes: len(frame.Data)})
		case "echo_ack":
			if !sawEcho || frame.Seq != 2 || frame.Code != 0 {
				t.Fatalf("ack %+v before echo=%v", frame, sawEcho)
			}
			return
		}
	}
}

func TestEchoAckReportsBusyAgent(t *testing.T) {
	s, _ := startEchoSession(t, "silent", []string{CapabilityEchoAck})
	sent := time.Now()
	s.send(Frame{Type: "input", Data: []byte("x"), Seq: 1})
	for {
		frame := s.next()
		if frame.Type == "echo_ack" {
			if frame.Seq != 1 || frame.Code != 1 || time.Since(sent) < echoTimeout-20*time.Millisecond {
				t.Fatalf("busy ack %+v after %v", frame, time.Since(sent))
			}
			return
		}
	}
}

func TestEchoAckRequiresFeature(t *testing.T) {
	s, receipt := startEchoSession(t, "echo", nil)
	if slices.Contains(receipt.Capabilities, CapabilityEchoAck) {
		t.Fatal("echo ack advertised without request")
	}
	s.send(Frame{Type: "input", Data: []byte("a"), Seq: 1})
	deadline := time.After(600 * time.Millisecond)
	frames := make(chan Frame)
	go func() {
		for {
			var frame Frame
			if s.decoder.Decode(&frame) != nil {
				close(frames)
				return
			}
			frames <- frame
		}
	}()
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if frame.Type == "echo_ack" {
				t.Fatal("old clients must never receive echo_ack")
			}
		case <-deadline:
			return
		}
	}
}

func TestInputCadenceSpacesBunchedKeys(t *testing.T) {
	s, _ := startEchoSession(t, "echo", []string{CapabilityInputCadence})
	for i, key := range []string{"a", "b", "c"} {
		s.send(Frame{Type: "input", Data: []byte(key), Seq: uint64(i + 1), DelayMS: 40})
	}
	var arrivals []time.Time
	for len(arrivals) < 3 {
		frame := s.next()
		if frame.Type == "data" {
			for range frame.Data {
				arrivals = append(arrivals, time.Now())
			}
			s.send(Frame{Type: "credit", Bytes: len(frame.Data)})
		}
	}
	if gap := arrivals[2].Sub(arrivals[0]); gap < 60*time.Millisecond {
		t.Fatalf("three bunched keys arrived within %v; want paced by ~40ms", gap)
	}
}

func TestEchoAckWaitsForOutputAndDoesNotReattributeOldPaint(t *testing.T) {
	a := newEchoAcker()
	a.input(1)
	finish := a.beginOutput()
	if _, ok := a.due(time.Now().Add(echoQuiet)); ok {
		t.Fatal("confirmed output still waiting for wire credit")
	}
	frame, ok := a.due(time.Now().Add(echoTimeout))
	if !ok || frame.Code != 1 {
		t.Fatal("blocked output must report busy rather than confirmed")
	}
	// New input can arrive while the old output is being written. Finishing
	// that write is not a reaction to the new input.
	a.input(2)
	finish()
	frame, ok = a.due(time.Now().Add(echoTimeout))
	if !ok || frame.Seq != 2 || frame.Code != 1 {
		t.Fatal("old paint was attributed to new input")
	}
	a.input(3)
	finish = a.beginOutput()
	finish()
	frame, ok = a.due(time.Now().Add(echoQuiet))
	if !ok || frame.Seq != 3 || frame.Code != 0 {
		t.Fatal("completed output was not confirmed")
	}
}
