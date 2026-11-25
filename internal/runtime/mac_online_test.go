package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/localssh"
	"strings"
	"testing"
)

func onlineFixture(address, state, agent string) LocalTerminal {
	status, _ := json.Marshal(map[string]string{"state": state, "app": agent})
	return LocalTerminal{Address: address, State: "live", Status: status, Labels: map[string]string{"fennec.session": "fresh-grade", "fennec.agent": agent}}
}

func TestMacOnlineOutboxDefersDeduplicatesAndFences(t *testing.T) {
	terminals := []LocalTerminal{onlineFixture("t1", "idle", "codex"), onlineFixture("t2", "blocked", "claude"), onlineFixture("t3", "idle", "bash"), onlineFixture("t4", "working", "codex")}
	sent := map[string]int{}
	probes := 0
	r := &LocalRuntime{Home: t.TempDir(), Query: func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method == "terminal.list" {
			return json.Marshal(map[string]any{"terminals": terminals})
		}
		if method != "terminal.send" {
			t.Fatal(method)
		}
		p := params.(map[string]any)
		if p["force"] != false || (p["if"] != "idle" && p["if"] != "working") {
			t.Fatal("unsafe submission", p)
		}
		text := p["text"].(string)
		if !strings.Contains(text, "Mac is back online.") || !strings.Contains(text, "<fennec-event") {
			t.Fatal(text)
		}
		for _, char := range text {
			if char > 127 {
				t.Fatal("notification must be English ASCII")
			}
		}
		if probes == 0 {
			t.Fatal("sent before end-to-end probe")
		}
		sent[p["terminal"].(string)]++
		return json.RawMessage(`{"started":true}`), nil
	}}
	h := localssh.LinkHealth{Device: "aabbccdd", Generation: strings.Repeat("a", 32), Since: 100}
	deliver := func(h localssh.LinkHealth) {
		t.Helper()
		if _, e := r.deliverMacOnline(context.Background(), h, func() error { probes++; return nil }); e != nil {
			t.Fatal(e)
		}
	}
	deliver(h)
	deliver(h)
	if sent["t1"] != 1 || sent["t2"] != 0 || sent["t3"] != 0 || sent["t4"] != 1 {
		t.Fatal(sent)
	}
	// A replacement resident loads the same on-disk outbox.
	r = &LocalRuntime{Home: r.Home, Query: r.Query}
	terminals[1] = onlineFixture("t2", "idle", "claude")
	deliver(h)
	if sent["t2"] != 1 {
		t.Fatal("deferred message missing", sent)
	}
	newer := h
	newer.Generation = strings.Repeat("b", 32)
	newer.Since = 200
	deliver(newer)
	deliver(h)
	if sent["t1"] != 2 || sent["t2"] != 1 {
		t.Fatal("obsolete event replayed", sent)
	}
}

func TestMacOnlineReachesAgentsByTheirReportedName(t *testing.T) {
	// Claude Code reports app=claude-code while Fennec labels it claude.
	claude := onlineFixture("t1", "idle", "claude")
	claude.Status = json.RawMessage(`{"state":"idle","app":"claude-code"}`)
	sent := 0
	r := &LocalRuntime{Home: t.TempDir(), Query: func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method == "terminal.list" {
			return json.Marshal(map[string]any{"terminals": []LocalTerminal{claude}})
		}
		sent++
		return json.RawMessage(`{"started":true}`), nil
	}}
	h := localssh.LinkHealth{Device: "aabbccdd", Generation: strings.Repeat("a", 32), Since: 100}
	if _, err := r.deliverMacOnline(context.Background(), h, func() error { return nil }); err != nil || sent != 1 {
		t.Fatal(err, sent)
	}
}

func TestMacOnlineNeverRetriesUncertainInput(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "refused"}[known], func(t *testing.T) {
			calls := 0
			r := &LocalRuntime{Home: t.TempDir(), Query: func(_ context.Context, method string, _ any) (json.RawMessage, error) {
				if method == "terminal.list" {
					return json.Marshal(map[string]any{"terminals": []LocalTerminal{onlineFixture("t1", "idle", "codex")}})
				}
				calls++
				if known {
					return decodeHerdrReply([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"data":{"kind":"status_mismatch"}}}`))
				}
				return nil, errors.New("receipt lost")
			}}
			h := localssh.LinkHealth{Device: "aabbccdd", Generation: strings.Repeat("a", 32), Since: 100}
			for range 2 {
				if _, e := r.deliverMacOnline(context.Background(), h, func() error { return nil }); e != nil {
					t.Fatal(e)
				}
			}
			expected := 1
			if known {
				expected = 2
			}
			if calls != expected {
				t.Fatal(calls)
			}
		})
	}
}

func TestMacOnlineDoesNotSendAfterProbeFailure(t *testing.T) {
	r := &LocalRuntime{Home: t.TempDir(), Query: func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method != "terminal.list" {
			t.Fatal("sent offline")
		}
		return json.Marshal(map[string]any{"terminals": []LocalTerminal{onlineFixture("t1", "idle", "codex")}})
	}}
	_, err := r.deliverMacOnline(context.Background(), localssh.LinkHealth{Device: "aabbccdd", Generation: strings.Repeat("a", 32), Since: 100}, func() error { return errors.New("Mac offline") })
	if err == nil {
		t.Fatal("failed probe accepted")
	}
}
