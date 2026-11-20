package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/launcher"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFenResumeReceiptStaysWithinCurrentRuntime(t *testing.T) {
	home := t.TempDir()
	id := strings.Repeat("c", 32)
	receipt := launcher.LaunchReceipt{RuntimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", Address: "t12", SessionID: id, Agent: "codex"}
	if err := launcher.SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt); err != nil {
		t.Fatal(err)
	}
	service := fenSessionService{home: home, runtimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543f", control: func(context.Context) (string, error) { t.Fatal("cross-runtime receipt reused"); return "", nil }}
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "resume", SessionID: id}); reply.Error == "" {
		t.Fatal("wrong runtime accepted")
	}
	service.runtimeID = receipt.RuntimeID
	service.control = func(context.Context) (string, error) { return "private-control", nil }
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "resume", SessionID: id}); reply.Error != "" || reply.Receipt == nil || reply.Receipt.SessionID != id {
		t.Fatal(reply)
	}
}

func TestFenCachedListNeverTouchesRemote(t *testing.T) {
	session := fenSession{Receipt: launcher.LaunchReceipt{SessionID: strings.Repeat("a", 32), Agent: "codex"}, Title: "Live task", Status: "Idle"}
	service := fenSessionService{sessions: []fenSession{session}, refresh: make(chan struct{}, 1), execute: func(context.Context, string) ([]byte, error) {
		t.Fatal("cached list accessed network")
		return nil, nil
	}}
	for _, operation := range []string{"list", "refresh"} {
		reply := service.handle(context.Background(), fenServiceRequest{Operation: operation})
		if len(reply.Sessions) != 1 || reply.Sessions[0].Title != "Live task" {
			t.Fatal(reply)
		}
	}
	if len(service.refresh) != 1 {
		t.Fatal("refresh request was not queued")
	}
	reply := service.handle(context.Background(), fenServiceRequest{Operation: "resume", SessionID: strings.Repeat("b", 32)})
	if reply.Error == "" {
		t.Fatal("unknown session accepted")
	}
}

func TestFenRefreshCannotResurrectKilledSession(t *testing.T) {
	id := strings.Repeat("a", 32)
	runtimeID := "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e"
	session := fenSession{Receipt: launcher.LaunchReceipt{RuntimeID: runtimeID, SessionID: id, Address: "t1", Agent: "codex"}, Title: "Task", Status: "Idle"}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	service := fenSessionService{runtimeID: runtimeID, sessions: []fenSession{session}, refresh: make(chan struct{}, 1)}
	service.execute = func(_ context.Context, script string) ([]byte, error) {
		if strings.Contains(script, "fennec-terminal\" list") {
			close(started)
			<-release
			data, _ := json.Marshal(map[string]any{"terminals": []map[string]any{{"address": "t1", "state": "live", "agent": "codex", "labels": map[string]string{"fennec.session": id}}}})
			return data, nil
		}
		return []byte(`{"state":"finished"}`), nil
	}
	go func() { defer close(done); service.update(context.Background()) }()
	<-started
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "list"}); len(reply.Sessions) != 1 || !reply.Refreshing {
		t.Fatal(reply)
	}
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "kill", SessionID: id}); reply.Error != "" {
		t.Fatal(reply)
	}
	close(release)
	<-done
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "list"}); len(reply.Sessions) != 0 {
		t.Fatal("stale refresh resurrected session", reply)
	}
}

func TestStopUsesPersistentChannelAndChecksRuntime(t *testing.T) {
	id := strings.Repeat("a", 32)
	calls := 0
	service := fenSessionService{runtimeID: "owned", stop: func(_ context.Context, receipt launcher.LaunchReceipt) (map[string]float64, error) {
		calls++
		if receipt.SessionID != id || receipt.Address != "t1" {
			t.Fatal(receipt)
		}
		return map[string]float64{"stop_confirm": 2}, nil
	}, execute: func(context.Context, string) ([]byte, error) { t.Fatal("stop opened exec channel"); return nil, nil }}
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "kill", RuntimeID: "other", Address: "t1", SessionID: id}); reply.Error == "" {
		t.Fatal("cross-runtime stop accepted")
	}
	if reply := service.handle(context.Background(), fenServiceRequest{Operation: "kill", RuntimeID: "owned", Address: "t1", SessionID: id}); reply.Error != "" || reply.Timings["stop_confirm"] != 2 {
		t.Fatal(reply)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestFenAttachNewLoginReceiptBeforeListRefresh(t *testing.T) {
	home := t.TempDir()
	receipt := launcher.LaunchReceipt{RuntimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", Address: "t12", SessionID: "login-fixture", Agent: "codex"}
	if err := launcher.SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt); err != nil {
		t.Fatal(err)
	}
	service := fenSessionService{home: home, runtimeID: receipt.RuntimeID, resume: func(_ context.Context, got launcher.LaunchReceipt) (launcher.LaunchPlan, error) {
		if got != receipt {
			t.Fatal("wrong receipt")
		}
		return launcher.LaunchPlan{SessionID: got.SessionID, StreamPath: "/fixture"}, nil
	}}
	reply := service.handle(context.Background(), fenServiceRequest{Operation: "attach", SessionID: receipt.SessionID})
	if reply.Error != "" || reply.Plan == nil || reply.Plan.SessionID != receipt.SessionID {
		t.Fatal("new login required an unrelated list refresh", reply.Error)
	}
}

func TestSessionSnapshotDistinguishesLoadingFromConfirmedEmpty(t *testing.T) {
	service := &fenSessionService{refresh: make(chan struct{}, 1)}
	channel := &terminalChannel{service: service}
	decode := func(raw json.RawMessage) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := decode(channel.cachedListing(true))
	if first["loaded"] != false || first["refreshing"] != true {
		t.Fatalf("initial snapshot: %v", first)
	}
	channel.cachedListing(true)
	if len(service.refresh) != 1 {
		t.Fatal("background refresh requests did not coalesce")
	}
	<-service.refresh
	service.listing = json.RawMessage(`{"terminals":[],"gateway":{"status":"ready"}}`)
	empty := decode(channel.cachedListing(false))
	if empty["loaded"] != true || empty["refreshing"] != false || empty["gateway"].(map[string]any)["status"] != "ready" {
		t.Fatalf("confirmed empty snapshot: %v", empty)
	}
}

func TestSessionSnapshotReturnsExistingRowsDuringBlockedRefresh(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	service := &fenSessionService{refresh: make(chan struct{}, 1), listing: json.RawMessage(`{"terminals":[{"address":"t1","state":"live"}]}`)}
	service.list = func(context.Context) ([]byte, error) { close(started); <-release; return nil, errors.New("offline") }
	channel := &terminalChannel{service: service}
	go func() { defer close(done); service.update(context.Background()) }()
	<-started
	result := make(chan json.RawMessage, 1)
	go func() { result <- channel.cachedListing(true) }()
	select {
	case raw := <-result:
		var listing struct {
			Loaded, Refreshing bool
			Terminals          []struct{ Address string }
		}
		if err := json.Unmarshal(raw, &listing); err != nil {
			t.Fatal(err)
		}
		if !listing.Loaded || !listing.Refreshing || len(listing.Terminals) != 1 || listing.Terminals[0].Address != "t1" {
			t.Fatalf("cached rows lost: %s", raw)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("snapshot waited for remote refresh")
	}
	close(release)
	<-done
	var listing struct {
		Loaded    bool
		Terminals []struct{ Address string }
	}
	raw := channel.cachedListing(false)
	if json.Unmarshal(raw, &listing) != nil || !listing.Loaded || len(listing.Terminals) != 1 {
		t.Fatalf("failed refresh erased cached rows: %s", raw)
	}
}
