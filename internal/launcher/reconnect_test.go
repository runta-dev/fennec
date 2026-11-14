package launcher

import (
	"context"
	"encoding/json"
	"errors"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstate"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

const recoverySession = "reconnect-fixture"

func recoveryAgent(mode string) string {
	if mode == "frame-claude" {
		return "claude"
	}
	return "codex"
}

func TestRecoveringTerminalChild(t *testing.T) {
	home := os.Getenv("FENNEC_RECOVERY_HOME")
	if home == "" {
		return
	}
	mode := os.Getenv("FENNEC_RECOVERY_MODE")
	agent := recoveryAgent(mode)
	plan := LaunchPlan{RuntimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", SessionID: recoverySession, Agent: agent, Resume: true, StreamPath: filepath.Join(home, ".config/fennec/ipc/terminal-"+recoverySession)}
	calls := 0
	recovery := StreamRecovery{Plan: func(ctx context.Context, receipt LaunchReceipt) (LaunchPlan, error) {
		calls++
		if receipt.SessionID != recoverySession || receipt.Address != "t1" || receipt.Agent != agent {
			panic("wrong session")
		}
		_ = os.WriteFile(filepath.Join(home, "attempt"), []byte(fmt.Sprint(calls)), 0600)
		switch mode {
		case "frame-codex", "frame-claude":
			if calls == 1 {
				return LaunchPlan{}, errors.New("connection lost")
			}
			select {
			case <-ctx.Done():
				return LaunchPlan{}, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		case "quiet":
			select {
			case <-ctx.Done():
				return LaunchPlan{}, ctx.Err()
			case <-time.After(2400 * time.Millisecond):
			}
		case "cancel-plan":
			<-ctx.Done()
			return LaunchPlan{}, ctx.Err()
		case "cancel-wait":
			return LaunchPlan{}, errors.New("daemon temporarily unavailable")
		case "identity":
			changed := plan
			changed.SessionID = "another-session"
			return changed, nil
		case "recover":
			if calls == 1 {
				return LaunchPlan{}, errors.New("daemon restarting")
			}
			if calls == 2 {
				return LaunchPlan{RuntimeID: plan.RuntimeID, SessionID: plan.SessionID, Agent: plan.Agent, Resume: true, StreamID: "missing-stream", StreamPath: filepath.Join(home, ".config/fennec/ipc/terminal-missing-stream")}, nil
			}
		}
		return plan, nil
	}}
	if mode == "wake" {
		recovery.Plan = func(context.Context, LaunchReceipt) (LaunchPlan, error) {
			calls++
			if calls == 1 {
				return LaunchPlan{}, errors.New("offline")
			}
			return plan, nil
		}
		recovery.Wake = func(ctx context.Context) <-chan struct{} {
			wake := make(chan struct{}, 1)
			go func() {
				select {
				case <-ctx.Done():
				case <-time.After(150 * time.Millisecond):
					wake <- struct{}{}
				}
			}()
			return wake
		}
	}
	err := ExecuteStream(plan, LaunchIntent{RequestID: recoverySession, Agent: agent}, home, func() {}, recovery)
	if detached, ok := err.(DetachedError); ok {
		fmt.Fprint(os.Stdout, detached.Notice())
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stdout, "RESULT_ERROR", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "RESULT_OK")
	os.Exit(0)
}

func TestReconnectRestoresSameSessionAndDropsDisconnectedInput(t *testing.T) {
	testRecoveryPTY(t, "recover")
}
func TestBriefReconnectPreservesScreen(t *testing.T)               { testRecoveryPTY(t, "brief") }
func TestReconnectStaysQuietForThreeSeconds(t *testing.T)          { testRecoveryPTY(t, "quiet") }
func TestReconnectRetainsCodexNormalScreen(t *testing.T)           { testRecoveryPTY(t, "frame-codex") }
func TestReconnectRetainsClaudeAlternateScreen(t *testing.T)       { testRecoveryPTY(t, "frame-claude") }
func TestReconnectWakesWhenConnectionReturns(t *testing.T)         { testRecoveryPTY(t, "wake") }
func TestReconnectCtrlCDuringPlan(t *testing.T)                    { testRecoveryPTY(t, "cancel-plan") }
func TestReconnectCtrlCDuringBackoff(t *testing.T)                 { testRecoveryPTY(t, "cancel-wait") }
func TestReconnectCtrlCDuringHandshake(t *testing.T)               { testRecoveryPTY(t, "cancel-handshake") }
func TestReconnectRejectsChangedIdentity(t *testing.T)             { testRecoveryPTY(t, "identity") }
func TestReconnectDoesNotRestartFinishedAgent(t *testing.T)        { testRecoveryPTY(t, "finished") }
func TestReconnectStopsOnAuthoritativeMissingSession(t *testing.T) { testRecoveryPTY(t, "missing") }

func testRecoveryPTY(t *testing.T, mode string) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "fn-rec-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	directory := filepath.Join(home, ".config/fennec/ipc")
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(directory, "terminal-"+recoverySession), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connections atomic.Int32
	var staleInput atomic.Bool
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			index := connections.Add(1)
			func() {
				defer conn.Close()
				// A daemon can disappear before even reading the reattach frame.
				// Both a failed connect write and a failed receipt read must retry.
				if index == 2 && mode == "recover" {
					return
				}
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				dec := json.NewDecoder(conn)
				enc := json.NewEncoder(conn)
				var hello terminalstream.Frame
				if dec.Decode(&hello) != nil {
					return
				}
				if hello.Type != "connect" || hello.ID != recoverySession {
					return
				}
				if index > 1 && mode == "cancel-handshake" {
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				if index > 1 && mode == "missing" {
					_ = enc.Encode(terminalstream.Frame{Type: "error", ID: hello.ID, ErrorCode: "session_unavailable", Message: "This cloud session is no longer available."})
					return
				}
				_ = enc.Encode(terminalstream.Frame{Type: "receipt", ID: hello.ID, Terminal: &cloudruntime.LocalTerminal{ID: recoverySession, Agent: recoveryAgent(mode), Address: "t1", State: "live"}})
				if index == 1 {
					frame := "\x1b[?1049h\x1b[2J\x1b[HConversation history\r\nLast assistant response\x1b[24;1H> Unsent draft\x1b[28;1HORIGINAL_SCREEN"
					if mode == "frame-codex" {
						frame = strings.TrimPrefix(frame, "\x1b[?1049h")
					}
					_ = enc.Encode(terminalstream.Frame{Type: "data", ID: hello.ID, Data: []byte(frame)})
					// Wait until the client consumed output before simulating an outage.
					var credit terminalstream.Frame
					_ = dec.Decode(&credit)
					if mode == "finished" {
						_ = enc.Encode(terminalstream.Frame{Type: "exit", ID: hello.ID})
					}
					return
				}
				_ = enc.Encode(terminalstream.Frame{Type: "data", ID: hello.ID, Data: []byte("RESTORED_SCREEN")})
				for {
					var f terminalstream.Frame
					if dec.Decode(&f) != nil {
						return
					}
					if f.Type == "input" {
						if strings.Contains(string(f.Data), "OFFLINE") {
							staleInput.Store(true)
						}
						if strings.Contains(string(f.Data), "ONLINE") {
							_ = enc.Encode(terminalstream.Frame{Type: "exit", ID: hello.ID})
							return
						}
					}
				}
			}()
		}
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestRecoveringTerminalChild$")
	child.Env = append(os.Environ(), "FENNEC_RECOVERY_HOME="+home, "FENNEC_RECOVERY_MODE="+mode, "TERM=xterm-256color")
	if mode == "recover" {
		child.Env = append(child.Env, "COLORTERM=truecolor", "NO_COLOR=")
	}
	terminal, err := pty.StartWithSize(child, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	var output strings.Builder
	buffer := make([]byte, 4096)
	sentOffline, sentOnline, sentCancel := false, false, false
	sentPalette := false
	var cancelledAt time.Time
	var detachLatency time.Duration
	var originalAt, progressAt, restoredAt time.Time
	for {
		n, readErr := terminal.Read(buffer)
		if n > 0 {
			output.Write(buffer[:n])
			text := output.String()
			if mode == "recover" && !sentPalette && strings.Contains(text, "\x1b]10;?") {
				palette := terminalstream.DisplayFacts{Foreground: "#cccccc", Background: "#181818"}
				terminal.Write(append(palette.Replies(), []byte("\x1b[?1;2c")...))
				sentPalette = true
			}
			if originalAt.IsZero() && strings.Contains(text, "ORIGINAL_SCREEN") {
				originalAt = time.Now()
			}
			if progressAt.IsZero() && strings.Contains(text, "Ctrl+C to detach") {
				progressAt = time.Now()
			}
			if restoredAt.IsZero() && strings.Contains(text, "RESTORED_SCREEN") {
				restoredAt = time.Now()
			}
			if sentCancel && detachLatency == 0 && strings.Contains(text, "fen attach "+recoverySession) {
				detachLatency = time.Since(cancelledAt)
			}
			if strings.Contains(text, "Retrying in") && mode == "recover" && !sentOffline {
				_, _ = terminal.Write([]byte("OFFLINE\r"))
				sentOffline = true
			}
			if strings.Contains(text, "RESTORED_SCREEN") && !sentOnline {
				_, _ = terminal.Write([]byte("ONLINE\r"))
				sentOnline = true
			}
			if strings.HasPrefix(mode, "cancel-") && !sentCancel && strings.Contains(text, "Ctrl+C to detach") {
				// Exercise both the traditional and enhanced terminal key encoding.
				key := []byte{3}
				if mode == "cancel-handshake" {
					key = []byte("\x1b[99;5u")
				}
				_, _ = terminal.Write(key)
				sentCancel = true
				cancelledAt = time.Now()
			}
		}
		if readErr != nil {
			break
		}
	}
	waitErr := child.Wait()
	_ = listener.Close()
	<-serverDone
	text := output.String()
	errorExpected := mode == "identity" || mode == "missing"
	if (waitErr != nil) != errorExpected {
		t.Fatalf("child: %v; output %q", waitErr, text)
	}
	if strings.HasPrefix(mode, "cancel-") {
		if !sentCancel || detachLatency == 0 || detachLatency > 500*time.Millisecond {
			t.Fatalf("cancellation was not immediate: %s %q", detachLatency, text)
		}
		reset, notice := strings.LastIndex(text, terminalstate.Reset), strings.Index(text, "fen attach "+recoverySession)
		if reset < 0 || notice < reset {
			t.Fatalf("detach notice must follow terminal restoration: %q", text)
		}
	} else if mode == "identity" || mode == "missing" {
		if !strings.Contains(text, "RESULT_ERROR") {
			t.Fatal(text)
		}
	} else {
		if !strings.Contains(text, "RESULT_OK") {
			t.Fatal(text)
		}
		if mode != "finished" {
			restored := strings.Index(text, "RESTORED_SCREEN")
			reset := strings.Index(text, terminalstate.Reset)
			if restored < 0 || reset < restored {
				t.Fatalf("screen restored too early: %q", text)
			}
		}
	}
	if mode == "recover" && (!sentOffline || connections.Load() < 3 || staleInput.Load()) {
		t.Fatalf("recovery or input isolation failed: offline=%v connections=%d stale=%v %q", sentOffline, connections.Load(), staleInput.Load(), text)
	}
	if mode == "recover" && !strings.Contains(text, "\x1b[38;2;") {
		t.Fatalf("true-color reconnect shimmer was not rendered: %q", text)
	}
	if mode == "brief" || mode == "wake" || mode == "quiet" {
		if strings.Contains(text, "Retrying in") || strings.Contains(text, "Ctrl+C to detach") || restoredAt.IsZero() || restoredAt.Sub(originalAt) >= reconnectGrace {
			t.Fatalf("brief recovery disturbed the terminal: %q", text)
		}
	}
	if mode == "finished" && connections.Load() != 1 {
		t.Fatalf("finished session reattached: %d", connections.Load())
	}
	if strings.HasPrefix(mode, "frame-") {
		start := strings.Index(text, "ORIGINAL_SCREEN") + len("ORIGINAL_SCREEN")
		end := strings.Index(text, "RESTORED_SCREEN")
		if end < start {
			t.Fatal("missing restored frame")
		}
		outage := text[start:end]
		if !strings.Contains(outage, "Ctrl+C to detach") {
			t.Fatal("test did not reach visible recovery")
		}
		for _, forbidden := range []string{"\x1b[2J", "\x1b[3J", "\x1bc", "\x1b[?1049", "\x1b[?1047", "\x1b[?47", "\r", "\n"} {
			if strings.Contains(outage, forbidden) {
				t.Fatalf("last frame disturbed during outage: %q", outage)
			}
		}
		for _, cursor := range regexp.MustCompile("\x1b\\[[0-9]+;[0-9]+H").FindAllString(outage, -1) {
			if cursor != "\x1b[30;1H" {
				t.Fatalf("recovery painted outside the bottom status row: %q", cursor)
			}
		}
	}
	if !progressAt.IsZero() {
		if progressAt.Sub(originalAt) < reconnectGrace-20*time.Millisecond {
			t.Fatalf("retry UI appeared before the three-second grace: %s", progressAt.Sub(originalAt))
		}
	}
	if strings.Contains(text, "Retry #") {
		t.Fatalf("retry count should not be shown: %q", text)
	}
}

func TestReconnectBackoffRemainsBoundedForLongOutages(t *testing.T) {
	base := time.Second
	for i := 0; i < 10000; i++ {
		delay := reconnectDelay(base)
		if delay <= 0 || delay > 5*time.Second {
			t.Fatalf("invalid retry delay %s", delay)
		}
		base = min(base*2, reconnectCap)
	}
}
