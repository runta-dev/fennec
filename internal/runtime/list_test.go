package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSessionListUsesOneCompleteSnapshot(t *testing.T) {
	calls := 0
	runtime := &LocalRuntime{Query: func(_ context.Context, method string, params any) (json.RawMessage, error) {
		calls++
		if method != "terminal.list" {
			t.Fatal(method)
		}
		return json.RawMessage(`{"terminals":[{"address":"t1","state":"live","title":"Fix login redirect","status":"working","labels":{"fennec.session":"quiet-river","where":"workspace"},"argv":["/bin/codex","private-input"],"foreground":{"argv":["private-input"]}},{"address":"t2","state":"live"}],"hidden_finished":3,"machines":[{"name":"peer","state":"connected","via":["private-input"]}]}`), nil
	}, Run: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("snapshot must not issue per-terminal CLI queries")
		return nil, nil
	}}
	output, err := runtime.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Terminals []map[string]any `json:"terminals"`
		Hidden    int              `json:"hidden_finished"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Hidden != 3 || len(result.Terminals) != 2 {
		t.Fatal("lost fallback row")
	}
	if result.Terminals[0]["title"] != "Fix login redirect" || result.Terminals[0]["status"] != "working" {
		t.Fatal(result)
	}
	if result.Terminals[1]["address"] != "t2" || result.Terminals[1]["state"] != "live" {
		t.Fatal(result)
	}
	if bytes.Contains(output, []byte("private-input")) || result.Terminals[0]["agent"] != "codex" || result.Terminals[0]["where"] != "workspace" {
		t.Fatal(string(output))
	}
}

func TestSessionListFailureDoesNotReturnPartialSnapshot(t *testing.T) {
	failure := errors.New("snapshot unavailable")
	r := &LocalRuntime{Query: func(context.Context, string, any) (json.RawMessage, error) { return nil, failure }}
	if raw, err := r.List(context.Background()); !errors.Is(err, failure) || raw != nil {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestHerdrReplyRequiresMatchingSuccessfulResponse(t *testing.T) {
	for _, raw := range []string{`{}`, `{"jsonrpc":"2.0","id":2,"result":{}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":1}}`, `{"jsonrpc":"2.0","id":1,"result":null}`, `{"jsonrpc":"2.0","id":1,"result":{}} {}`} {
		if _, err := decodeHerdrReply([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if raw, err := decodeHerdrReply([]byte(`{"jsonrpc":"2.0","id":1,"result":{"terminals":[]}}`)); err != nil || string(raw) != `{"terminals":[]}` {
		t.Fatalf("%s %v", raw, err)
	}
}
