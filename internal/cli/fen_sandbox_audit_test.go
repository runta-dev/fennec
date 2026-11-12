package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/audit"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFenSandboxAndAuditCLICommandsEndToEnd(t *testing.T) {
	ipcHome, err := os.MkdirTemp("/tmp", "fen-sbx-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ipcHome)
	t.Setenv("HOME", ipcHome)

	workspaceDir := filepath.Join(ipcHome, "project-alpha")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		t.Fatal(err)
	}

	settings := newDesktopSettings(ipcHome)
	auditStore := audit.NewStore(ipcHome)
	network := connectivity.NewMonitor(func() bool { return true }, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controller := &controlplane.Controller{
		Account:      account.New(ctx, serviceCredentials{}, ""),
		Connectivity: network,
		Additional:   map[string]control.Handler{},
	}
	controller.Settings = func() any {
		prefs := settings.snapshot().(desktopPreferences)
		preset := effectivePermissionPreset(prefs)
		eff := effectiveSandboxPolicy(ipcHome, prefs)
		res, _ := auditStore.Query(audit.QueryOptions{Limit: 1})
		return map[string]any{
			"revision":                prefs.Revision,
			"permission_mode":         preset,
			"sandbox_mode":            eff.Mode,
			"sandbox_network":         eff.Network,
			"effective_sandbox_roots": eff.WritableRoots,
			"audit_summary":           res.Summary,
			"theme":                   prefs.Theme,
			"language":                prefs.Language,
			"initialized":             prefs.Initialized,
		}
	}
	controller.Additional["settings.get"] = func(context.Context, json.RawMessage) (any, error) { return controller.Settings(), nil }
	controller.Additional["settings.set"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		if _, err := settings.update(ctx, raw, nil); err != nil {
			return nil, err
		}
		return controller.Settings(), nil
	}
	controller.Additional["audit.list"] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var opts audit.QueryOptions
		if len(raw) > 0 && json.Unmarshal(raw, &opts) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid audit query"}
		}
		return auditStore.Query(opts)
	}
	controller.Additional["audit.clear"] = func(context.Context, json.RawMessage) (any, error) {
		if err := auditStore.Clear(); err != nil {
			return nil, err
		}
		return auditStore.Query(audit.QueryOptions{Limit: 1})
	}

	socket := filepath.Join(ipcHome, ".config/fennec/ipc/control.sock")
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- control.Serve(ctx, socket, controller.Handlers(), func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("control socket start timeout")
	}

	var out bytes.Buffer
	handled, err := fenControlCommand([]string{"sandbox", "status"}, &out)
	if !handled || err != nil {
		t.Fatalf("fen sandbox status failed: handled=%v err=%v", handled, err)
	}
	var state map[string]any
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["permission_mode"] != "auto" || state["sandbox_mode"] != "workspace" || state["sandbox_network"] != false {
		t.Fatalf("unexpected default sandbox state: %+v", state)
	}

	out.Reset()
	if _, err := fenControlCommand([]string{"sandbox", "ask"}, &out); err != nil {
		t.Fatalf("fen sandbox ask failed: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["permission_mode"] != "ask" || state["sandbox_mode"] != "workspace" || state["sandbox_network"] != false {
		t.Fatalf("expected ask preset (workspace + network=false), got %+v", state)
	}

	out.Reset()
	if _, err := fenControlCommand([]string{"sandbox", "full"}, &out); err != nil {
		t.Fatalf("fen sandbox full failed: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["permission_mode"] != "full" || state["sandbox_mode"] != "full" || state["sandbox_network"] != true {
		t.Fatalf("expected full preset (full + network=true), got %+v", state)
	}

	if err := auditStore.Record(audit.Entry{
		Kind:        "exec",
		Command:     "OPENAI_API_KEY=sk-secret1234567890 git status",
		Workdir:     workspaceDir,
		SandboxMode: "workspace",
		Decision:    "auto_sandbox",
		ExitCode:    0,
		DurationMS:  14,
	}); err != nil {
		t.Fatal(err)
	}
	if err := auditStore.Record(audit.Entry{
		Kind:        "exec",
		Command:     "cat ~/.ssh/id_ed25519",
		Workdir:     workspaceDir,
		SandboxMode: "workspace",
		Decision:    "sandbox_blocked",
		Blocked:     true,
		ExitCode:    1,
		DurationMS:  8,
		Detail:      "sandbox-exec: Operation not permitted",
	}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if _, err := fenControlCommand([]string{"audit", "--blocked"}, &out); err != nil {
		t.Fatalf("fen audit --blocked failed: %v", err)
	}
	var q audit.QueryResult
	if err := json.Unmarshal(out.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if q.Summary.Total7d != 2 || q.Summary.Blocked7d != 1 || len(q.Entries) != 1 || !q.Entries[0].Blocked {
		t.Fatalf("unexpected blocked audit query result: %+v", q)
	}

	out.Reset()
	if _, err := fenControlCommand([]string{"audit", "--approved"}, &out); err != nil {
		t.Fatalf("fen audit --approved failed: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Entries) != 1 || q.Entries[0].Command != "OPENAI_API_KEY=[REDACTED] git status" {
		t.Fatalf("expected redacted approved entry, got %+v", q.Entries)
	}

	out.Reset()
	if _, err := fenControlCommand([]string{"audit", "--clear"}, &out); err != nil {
		t.Fatalf("fen audit --clear failed: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if q.Summary.Total7d != 0 || len(q.Entries) != 0 {
		t.Fatalf("expected empty audit store after clear, got %+v", q)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
