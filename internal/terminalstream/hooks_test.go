package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/hookbridge"
	cloudruntime "fennec/internal/runtime"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCmuxAgentChild(t *testing.T) {
	if os.Getenv("FENNEC_CMUX_AGENT_CHILD") != "1" {
		return
	}
	result := hookbridge.Invoke(context.Background(), os.Getenv("HOME"), "petite-neat", strings.Repeat("a", 64), bytes.NewBufferString(`{"session_id":"fixture-session","hook_event_name":"SessionStart"}`))
	if !bytes.Contains(result.Stdout, []byte("DELIVERED")) {
		os.Exit(17)
	}
	os.Stdout.Write([]byte("HOOK_BRIDGE_OK"))
	os.Exit(0)
}
func TestCmuxHookTraversesResidentStreamWithoutBlockingPeer(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-cmux-stream-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer send.Close()
	defer receive.Close()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, input, output, func(_ context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			terminal := cloudruntime.LocalTerminal{ID: request.ID, Agent: "codex", Address: "t1", State: "live"}
			if request.ID == "gentle-fox" {
				return terminal, exec.Command("/bin/sh", "-c", "printf PEER_OK"), nil
			}
			executable, err := os.Executable()
			if err != nil {
				return terminal, nil, err
			}
			command := exec.Command(executable, "-test.run=^TestCmuxAgentChild$")
			command.Env = append(os.Environ(), "FENNEC_CMUX_AGENT_CHILD=1")
			return terminal, command, nil
		})
	}()
	decoder := json.NewDecoder(receive)
	encoder := json.NewEncoder(send)
	var frame Frame
	if err := decoder.Decode(&frame); err != nil || frame.Type != "ready" {
		t.Fatal(err)
	}
	opening := Frame{Type: "launch", ID: "petite-neat", HookBridge: true, Rows: 24, Columns: 80, Launch: &cloudruntime.LocalLaunch{ID: "petite-neat", Agent: "codex", Device: "1234abcd", LocalDirectory: "/project", Hooks: []hookbridge.Binding{testBinding()}}}
	if err := encoder.Encode(opening); err != nil {
		t.Fatal(err)
	}
	var hook Frame
	for {
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "hook_request" {
			hook = frame
			break
		}
		if frame.Type == "error" {
			t.Fatal(frame.Message)
		}
	}
	peer := Frame{Type: "launch", ID: "gentle-fox", Rows: 24, Columns: 80, Launch: &cloudruntime.LocalLaunch{ID: "gentle-fox", Agent: "codex", Device: "1234abcd", LocalDirectory: "/project", Hooks: []hookbridge.Binding{testBinding()}}}
	if err := encoder.Encode(peer); err != nil {
		t.Fatal(err)
	}
	peerVisible := false
	for !peerVisible {
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "data" {
			if err := encoder.Encode(Frame{Type: "credit", ID: frame.ID, Bytes: len(frame.Data)}); err != nil {
				t.Fatal(err)
			}
			peerVisible = frame.ID == "gentle-fox" && bytes.Contains(frame.Data, []byte("PEER_OK"))
		}
		if frame.Type == "error" {
			t.Fatal(frame.Message)
		}
	}
	// A reply naming a different stream must not satisfy this hook.
	encoder.Encode(Frame{Type: "hook_reply", ID: "gentle-fox", HookID: hook.HookID, Result: testHookResult("WRONG")})
	encoder.Encode(Frame{Type: "hook_reply", ID: hook.ID, HookID: hook.HookID, Result: testHookResult("DELIVERED")})
	var visible strings.Builder
	for {
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "data" {
			if frame.ID == opening.ID {
				visible.Write(frame.Data)
			}
			encoder.Encode(Frame{Type: "credit", ID: frame.ID, Bytes: len(frame.Data)})
		}
		if frame.Type == "exit" && frame.ID == opening.ID {
			if frame.Code != 0 {
				t.Fatal("hook did not receive its reply", frame.Code)
			}
			break
		}
	}
	if !strings.Contains(visible.String(), "HOOK_BRIDGE_OK") {
		t.Fatal("terminal did not remain usable")
	}
	cancel()
	send.Close()
	receive.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server survived shutdown")
	}
}

func TestCmuxConcurrentAttachmentsUseCurrentOwnerWithoutStealingPeer(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-cmux-attach-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := make(chan Frame, 4)
	relay := newHookRelay(ctx, func(frame Frame) error { frames <- frame; return nil })
	if err := hookbridge.SaveBindings(home, "petite-neat", []hookbridge.Binding{testBinding()}); err != nil {
		t.Fatal(err)
	}
	first, err := relay.bind(ctx, "calm-tiger", "petite-neat")
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := relay.bind(ctx, "bright-bird", "petite-neat")
	if err != nil {
		t.Fatal(err)
	}
	check := func(owner string) {
		t.Helper()
		done := make(chan hookbridge.Result, 1)
		go func() {
			done <- hookbridge.Invoke(ctx, home, "petite-neat", strings.Repeat("a", 64), bytes.NewBufferString(`{}`))
		}()
		var frame Frame
		select {
		case frame = <-frames:
		case <-ctx.Done():
			t.Fatal("hook not delivered")
		}
		if frame.ID != owner {
			t.Fatal("wrong attachment received hook", frame.ID)
		}
		relay.reply(Frame{ID: "unrelated-pane", HookID: frame.HookID, Result: testHookResult("bad")})
		select {
		case <-done:
			t.Fatal("foreign reply accepted")
		default:
		}
		relay.reply(Frame{ID: owner, HookID: frame.HookID, Result: testHookResult("correct")})
		if result := <-done; !bytes.Contains(result.Stdout, []byte("correct")) {
			t.Fatal("wrong reply")
		}
	}
	check("bright-bird")
	second()
	check("calm-tiger")
}

func testBinding() hookbridge.Binding {
	return hookbridge.Binding{ID: strings.Repeat("a", 64), Name: "Fixture", Event: "PreToolUse", Policy: hookbridge.Required, TimeoutMS: 1000}
}
func testHookResult(text string) json.RawMessage {
	raw, _ := json.Marshal(hookbridge.Result{Stdout: []byte(text), Status: "executed"})
	return raw
}
