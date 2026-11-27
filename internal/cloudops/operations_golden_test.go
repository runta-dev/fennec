package cloudops

import (
	"context"
	"encoding/json"
	"fennec/internal/board"
	"fennec/internal/localssh"
	"fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/tailnet"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateOperationsGolden = flag.Bool("update-operations-golden", false, "rewrite testdata/operations.golden.json")

// operateForGolden is the one line that changes when the dispatcher moves.
func operateForGolden(rt *runtime.LocalRuntime, o Operation) (json.RawMessage, error) {
	return Operate(context.Background(), rt, o)
}

// TestOperationsGolden pins every operation kind's validation, result and the
// herdr calls it makes, so moving the dispatcher cannot change what the Mac,
// the desktop window or the iPhone receive.
func TestOperationsGolden(t *testing.T) {
	id := strings.Repeat("a", 32)
	type call struct {
		Via  string `json:"via"`
		Args any    `json:"args"`
	}
	type outcome struct {
		Name   string          `json:"name"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  string          `json:"error,omitempty"`
		Calls  []call          `json:"calls"`
	}
	var got []outcome
	cases := []struct {
		name string
		op   Operation
		// noObserve drops Observe, as on a cloud binary without paint support.
		noObserve bool
	}{
		{name: "unknown kind", op: Operation{Kind: "format_disk"}},
		{name: "empty kind", op: Operation{}},
		{name: "thread_session invalid project", op: Operation{Kind: "thread_session", Project: "Bad Project", RequestNumber: 1}},
		{name: "thread_session invalid number", op: Operation{Kind: "thread_session", Project: "thread", RequestNumber: 0}},
		{name: "thread_session live", op: Operation{Kind: "thread_session", Project: "PROJECT", RequestNumber: -1}},
		{name: "thread_session missing request", op: Operation{Kind: "thread_session", Project: "PROJECT", RequestNumber: 99}},
		{name: "connector_health without health", op: Operation{Kind: "connector_health"}},
		{name: "connector_health invalid health", op: Operation{Kind: "connector_health", Health: &localssh.LinkHealth{}}},
		{name: "mac_online without generation", op: Operation{Kind: "mac_online", Health: &localssh.LinkHealth{Device: "1234abcd"}}},
		{name: "preparation_health without check", op: Operation{Kind: "preparation_health"}},
		{name: "preparation_health invalid check", op: Operation{Kind: "preparation_health", Preparation: &setup.PreparationHealth{}}},
		{name: "system_metrics", op: Operation{Kind: "system_metrics"}},
		{name: "tailnet_add without device", op: Operation{Kind: "tailnet_add"}},
		{name: "tailnet_add without user", op: Operation{Kind: "tailnet_add", Tailnet: &tailnet.Request{ID: "nKx7"}}},
		{name: "tailnet_add option as user", op: Operation{Kind: "tailnet_add", Tailnet: &tailnet.Request{ID: "nKx7", User: "-oProxyCommand=x"}}},
		{name: "tailnet_remove invalid device", op: Operation{Kind: "tailnet_remove", Tailnet: &tailnet.Request{ID: "../x"}}},
		{name: "cloud_agents", op: Operation{Kind: "cloud_agents"}},
		{name: "reconfigure_runtime without configuration", op: Operation{Kind: "reconfigure_runtime"}},
		{name: "native_account codex", op: Operation{Kind: "native_account", NativeModel: &setup.NativeModelSelection{Agent: "codex"}}},
		{name: "native_account agy", op: Operation{Kind: "native_account", NativeModel: &setup.NativeModelSelection{Agent: "agy"}}},
		{name: "native_account unsupported", op: Operation{Kind: "native_account", NativeModel: &setup.NativeModelSelection{Agent: "opencode"}}},
		{name: "native_models agy unsupported", op: Operation{Kind: "native_models", NativeModel: &setup.NativeModelSelection{Agent: "agy"}}},
		{name: "native_models codex", op: Operation{Kind: "native_models", NativeModel: &setup.NativeModelSelection{Agent: "codex"}}},
		{name: "native_model_set foreign model", op: Operation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex", Model: "kimi-code/k3"}}},
		{name: "native_model_set codex", op: Operation{Kind: "native_model_set", NativeModel: &setup.NativeModelSelection{Agent: "codex", Model: "gpt-test"}}},
		{name: "native_login_status invalid address", op: Operation{Kind: "native_login_status", Address: "x1", Login: &setup.NativeLogin{ID: "login-fixture", Agent: "codex"}}},
		{name: "native_login_status without login", op: Operation{Kind: "native_login_status", Address: "t1"}},
		{name: "native_login_status", op: Operation{Kind: "native_login_status", Address: "t3", Login: &setup.NativeLogin{ID: "login-fixture", Agent: "codex"}}},
		{name: "native_login_cancel", op: Operation{Kind: "native_login_cancel", Address: "t3", Login: &setup.NativeLogin{ID: "login-fixture", Agent: "codex"}}},
		{name: "native_credential_set without credential", op: Operation{Kind: "native_credential_set"}},
		{name: "native_credential_watch without watch", op: Operation{Kind: "native_credential_watch"}},
		{name: "model_accounts without accounts", op: Operation{Kind: "model_accounts"}},
		{name: "magpie_credentials_sync empty", op: Operation{Kind: "magpie_credentials_sync"}},
		{name: "connector_prepare without connector", op: Operation{Kind: "connector_prepare"}},
		{name: "connector_prepare invalid connector", op: Operation{Kind: "connector_prepare", Connector: &localssh.LinkBootstrap{}}},
		{name: "reconcile_routes empty", op: Operation{Kind: "reconcile_routes"}},
		{name: "list", op: Operation{Kind: "list"}},
		{name: "launch without request", op: Operation{Kind: "launch"}},
		{name: "launch invalid request", op: Operation{Kind: "launch", Launch: &runtime.LocalLaunch{ID: "x"}}},
		{name: "stop live", op: Operation{Kind: "stop", Address: "t1", SessionID: id}},
		{name: "stop finished", op: Operation{Kind: "stop", Address: "t2", SessionID: id}},
		{name: "stop invalid identity", op: Operation{Kind: "stop", Address: "t1", SessionID: "short"}},
		{name: "read", op: Operation{Kind: "read", Address: "t1", SessionID: id}},
		{name: "read wrong session", op: Operation{Kind: "read", Address: "t1", SessionID: strings.Repeat("b", 32)}},
		{name: "peek painted", op: Operation{Kind: "peek", Address: "t1", SessionID: id}},
		{name: "peek without painter", op: Operation{Kind: "peek", Address: "t1", SessionID: id}, noObserve: true},
		{name: "peek finished", op: Operation{Kind: "peek", Address: "t2", SessionID: id}},
		{name: "send", op: Operation{Kind: "send", Address: "t1", SessionID: id, Text: "hello"}},
		{name: "send blank", op: Operation{Kind: "send", Address: "t1", SessionID: id, Text: "  "}},
		{name: "send to finished", op: Operation{Kind: "send", Address: "t2", SessionID: id, Text: "hello"}},
	}
	for _, c := range cases {
		home := t.TempDir()
		catalog := filepath.Join(home, ".local/fennec/native/codex")
		if err := os.MkdirAll(catalog, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(catalog, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-test"},{"slug":"kimi-code/k3"}]}`), 0600); err != nil {
			t.Fatal(err)
		}
		store := board.Store{Home: home, Role: "cloud"}
		store.SetRouting(board.Routing{Device: "1234abcd"})
		p, err := store.CreateProject(board.Project{Name: "Thread", Queue: 1})
		if err != nil {
			t.Fatal(err)
		}
		request, err := store.CreateRequest(p.ID, "me", "Question", "", "Ask")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimAt(p.ID, request.Number, id, "codex", "cloud"); err != nil {
			t.Fatal(err)
		}
		killed := map[string]bool{}
		sessions := map[string]string{"t1": id, "t2": id, "t3": "login-fixture"}
		var calls []call
		state := func(address string) string {
			if address == "t2" || killed[address] {
				return "finished"
			}
			return "live"
		}
		rt := &runtime.LocalRuntime{Home: home,
			Lookup: func(name string) (string, error) { return "/fixture/" + name, nil },
			Run: func(_ context.Context, args ...string) ([]byte, error) {
				calls = append(calls, call{Via: "run", Args: args})
				switch args[0] {
				case "get":
					return json.Marshal(map[string]any{"address": args[1], "state": state(args[1]), "labels": map[string]string{"fennec.session": sessions[args[1]], "fennec.agent": "codex"}})
				case "read":
					return []byte(`{"text":"hello","complete":true}`), nil
				case "send":
					return []byte(`{"accepted":true}`), nil
				case "kill":
					killed[args[1]] = true
					return []byte(`{}`), nil
				case "ls":
					return []byte(`{"terminals":[]}`), nil
				}
				return []byte(`{"address":"t9","state":"live"}`), nil
			},
			Query: func(_ context.Context, method string, params any) (json.RawMessage, error) {
				calls = append(calls, call{Via: "query " + method, Args: params})
				if method == "terminal.list" {
					return json.RawMessage(`{"terminals":[` +
						`{"address":"t1","name":"one","state":"live","title":"Work","argv":["/usr/bin/codex","secret prompt"],"labels":{"fennec.session":"` + id + `","fennec.agent":"codex","where":"cloud"},"status":{"app":"codex","state":"idle"}},` +
						`{"address":"t2","state":"finished","argv":["/usr/local/bin/claude"],"labels":{}}` +
						`],"machines":[{"name":"m","state":"up","since_ms":1,"why":"ok","host":"10.0.0.1","key":"secret"}],"version":"x"}`), nil
				}
				return json.RawMessage(`{}`), nil
			},
		}
		if !c.noObserve {
			rt.Observe = func(_ context.Context, address string) ([]byte, error) {
				return []byte("\x1b[0mpaint " + address), nil
			}
		}
		op := c.op
		if op.Project == "PROJECT" {
			op.Project = p.ID
		}
		if op.RequestNumber == -1 {
			op.RequestNumber = request.Number
		}
		result, err := operateForGolden(rt, op)
		o := outcome{Name: c.name, Calls: calls}
		if err != nil {
			o.Error = err.Error()
		} else {
			o.Result = result
		}
		if c.op.Kind == "system_metrics" && err == nil {
			var fields map[string]any
			json.Unmarshal(result, &fields)
			keys := []string{}
			for k := range fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			o.Result, _ = json.Marshal(keys)
		}
		raw, _ := json.Marshal(o)
		raw = []byte(strings.ReplaceAll(string(raw), home, "$HOME"))
		raw = []byte(strings.ReplaceAll(string(raw), p.ID, "$PROJECT"))
		var normalized outcome
		json.Unmarshal(raw, &normalized)
		normalized.Result = stripTimings(normalized.Result)
		got = append(got, normalized)
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	golden := filepath.Join("testdata", "operations.golden.json")
	if *updateOperationsGolden {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, data, 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(data) {
		t.Fatalf("operation results changed; diff testdata/operations.golden.json against:\n%s", data)
	}
}

// stripTimings drops measured durations, the only nondeterministic fields.
func stripTimings(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			delete(v, "timings_ms")
			delete(v, "timings")
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(value)
	out, _ := json.Marshal(value)
	return out
}

// TestOperationWireFormat pins the operation's JSON: the Mac and the cloud
// computer run different builds, so field names and order must not move.
func TestOperationWireFormat(t *testing.T) {
	o := Operation{Health: &localssh.LinkHealth{}, MagpieCredentials: map[string]string{"a": "b"}, Connector: &localssh.LinkBootstrap{},
		Preparation: &setup.PreparationHealth{}, Configuration: &setup.RuntimeConfiguration{}, Login: &setup.NativeLogin{},
		NativeModel: &setup.NativeModelSelection{}, Accounts: &setup.ModelAccountsRequest{}, Credential: &setup.NativeCredentialPush{},
		CredentialWatch: &setup.NativeCredentialWatch{}, Routes: []setup.AgentRoute{{}}, Launch: &runtime.LocalLaunch{},
		Kind: "k", Address: "t1", SessionID: "s", Text: "x", Project: "p", RequestNumber: 2}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "operation_wire.golden.json")
	if *updateOperationsGolden {
		if err := os.WriteFile(golden, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(raw)+"\n" {
		t.Fatalf("wire format changed:\n%s\nwant:\n%s", raw, want)
	}
	var back Operation
	if err := json.Unmarshal(want, &back); err != nil || back.Kind != "k" || back.RequestNumber != 2 || back.Launch == nil {
		t.Fatalf("golden no longer decodes: %v %+v", err, back)
	}
}
