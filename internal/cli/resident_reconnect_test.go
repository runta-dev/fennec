package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fennec/internal/terminalstream"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecoveryReadinessWaitsForBrokerAndWakesAlreadyReadyClient(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-rec-ready-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, peerInput := io.Pipe()
	peerOutput, output := io.Pipe()
	transport := terminalstream.StartPipes(peerInput, peerOutput)
	defer transport.Close()
	defer input.Close()
	defer output.Close()
	go func() { io.Copy(io.Discard, input) }()
	go func() {
		json.NewEncoder(output).Encode(terminalstream.Frame{Type: "ready", Version: terminalstream.ProtocolVersion})
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, time.Second)
	defer readyCancel()
	if err := transport.Ready(readyCtx); err != nil {
		t.Fatal(err)
	}
	s := &daemonServices{channel: &terminalChannel{transport: transport, shared: &launcher.SharedConnection{}, generation: 1}}
	handlers := s.handlers(nil)
	status, err := handlers["connection.status"](ctx, nil)
	if err != nil || status.(map[string]any)["ready"] != false {
		t.Fatal("transport readiness exposed before broker readiness", status, err)
	}
	s.brokerReady = true
	stop := startRecoveryDaemon(t, home, handlers)
	defer stop()
	wake := watchResidentRecoveryEvents(ctx, home, nil)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("already-ready daemon did not wake reconnect")
	}
	select {
	case <-wake:
		t.Fatal("unchanged readiness repeatedly bypassed backoff")
	case <-time.After(650 * time.Millisecond):
	}
}

func startRecoveryDaemon(t *testing.T, home string, handlers map[string]control.Handler) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- control.Serve(ctx, filepath.Join(home, ".config/fennec/ipc/control.sock"), handlers, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("daemon did not start")
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop")
		}
	}
}

func TestResidentRecoverySurvivesControlSocketReplacement(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-rec-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	receipt := launcher.LaunchReceipt{RuntimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", SessionID: "recovery-fixture", Address: "t1", Agent: "codex"}
	var requests atomic.Int32
	var account atomic.Value
	account.Store("authorized")
	var revision atomic.Uint64
	revision.Store(1)
	var runtimeID atomic.Value
	runtimeID.Store(receipt.RuntimeID)
	handlers := map[string]control.Handler{
		"daemon.ping": func(context.Context, json.RawMessage) (any, error) { return map[string]any{}, nil },
		"state.get": func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"account": map[string]any{"status": account.Load(), "revision": revision.Load()}, "runtime": map[string]any{"runtime_id": runtimeID.Load()}, "services": map[string]any{"broker_ready": true}}, nil
		},
		"sessions.dispatch": func(_ context.Context, raw json.RawMessage) (any, error) {
			var request fenServiceRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			if request.Operation != "attach" || request.SessionID != receipt.SessionID || request.RuntimeID != receipt.RuntimeID || request.Address != receipt.Address {
				return nil, errors.New("unverified reattachment")
			}
			requests.Add(1)
			return fenServiceReply{Receipt: &receipt, Plan: &launcher.LaunchPlan{SessionID: receipt.SessionID, RuntimeID: receipt.RuntimeID, Agent: receipt.Agent, Resume: true, StreamPath: "/fixture"}}, nil
		},
	}
	stop := startRecoveryDaemon(t, home, handlers)
	if _, err := residentReconnectPlan(context.Background(), receipt, home); err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = residentReconnectPlan(ctx, receipt, home)
	cancel()
	var permanent launcher.ReconnectStopped
	if err == nil || errors.As(err, &permanent) {
		t.Fatalf("daemon restart was classified as permanent: %v", err)
	}
	stop = startRecoveryDaemon(t, home, handlers)
	defer stop()
	if _, err := residentReconnectPlan(context.Background(), receipt, home); err != nil {
		t.Fatal(err)
	}
	account.Store("signed_out")
	revision.Store(0)
	if _, err := residentReconnectPlan(context.Background(), receipt, home); err == nil || errors.As(err, &permanent) {
		t.Fatalf("startup state was mistaken for sign-out: %v", err)
	}
	revision.Store(1)
	if _, err := residentReconnectPlan(context.Background(), receipt, home); !errors.As(err, &permanent) {
		t.Fatalf("signed-out account was retried: %v", err)
	}
	account.Store("authorized")
	runtimeID.Store("different-runtime")
	if _, err := residentReconnectPlan(context.Background(), receipt, home); !errors.As(err, &permanent) {
		t.Fatalf("changed runtime was retried: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("unexpected reattachment count: %d", requests.Load())
	}
}

func TestResidentRecoveryRequestCancellation(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-rec-cancel-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	stop := startRecoveryDaemon(t, home, map[string]control.Handler{"sessions.dispatch": func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil, ctx.Err()
	}})
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := fenLocalRequestContext(ctx, home, fenServiceRequest{Operation: "attach", SessionID: "recovery-fixture"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach daemon")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation waited for RPC timeout")
	}
}

func TestResidentRecoveryWakeOnSharedConnectionTransition(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-rec-wake-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	var ready atomic.Bool
	probed := make(chan struct{}, 1)
	stop := startRecoveryDaemon(t, home, map[string]control.Handler{"connection.status": func(context.Context, json.RawMessage) (any, error) {
		select {
		case probed <- struct{}{}:
		default:
		}
		return map[string]any{"ready": ready.Load(), "generation": 1}, nil
	}})
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := watchResidentRecoveryEvents(ctx, home, nil)
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("missing initial probe")
	}
	// Let the initial false snapshot finish before publishing readiness.
	time.Sleep(20 * time.Millisecond)
	ready.Store(true)
	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("connection recovery did not wake the client")
	}
	select {
	case <-wake:
		t.Fatal("unchanged ready state bypasses backoff")
	case <-time.After(650 * time.Millisecond):
	}
}

func TestResidentRecoveryUsesKernelReadinessNotification(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-ready-event-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	var ready atomic.Bool
	probed := make(chan struct{}, 1)
	stop := startRecoveryDaemon(t, home, map[string]control.Handler{"connection.status": func(context.Context, json.RawMessage) (any, error) {
		select {
		case probed <- struct{}{}:
		default:
		}
		return map[string]any{"ready": ready.Load(), "generation": 1}, nil
	}})
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := watchResidentRecoveryEvents(ctx, home, nil)
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("missing initial probe")
	}
	time.Sleep(20 * time.Millisecond)
	// A hint alone must not claim readiness or bypass backoff.
	notifyConnectionReady(home)
	select {
	case <-wake:
		t.Fatal("unready hint woke attachment")
	case <-time.After(30 * time.Millisecond):
	}
	ready.Store(true)
	notifyConnectionReady(home)
	select {
	case <-wake:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readiness waited for the 500ms poll")
	}
}

func TestResidentRecoveryWakesOnNetworkEventWhileDaemonStillUnavailable(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-rec-event-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	probed := make(chan struct{}, 1)
	stop := startRecoveryDaemon(t, home, map[string]control.Handler{"connection.status": func(context.Context, json.RawMessage) (any, error) {
		select {
		case probed <- struct{}{}:
		default:
		}
		return map[string]any{"ready": false, "generation": 1}, nil
	}})
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan struct{}, 1)
	wake := watchResidentRecoveryEvents(ctx, home, events)
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("missing initial daemon probe")
	}
	events <- struct{}{}
	select {
	case <-wake:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("VPN/interface event did not interrupt retry wait")
	}
	select {
	case <-wake:
		t.Fatal("unchanged connectivity bypassed backoff")
	case <-time.After(550 * time.Millisecond):
	}
}
