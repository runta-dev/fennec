package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReadPreservesHerdrCompleteness(t *testing.T) {
	id := strings.Repeat("a", 32)
	runtime := &LocalRuntime{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "get" {
			return []byte(`{"labels":{"fennec.session":"` + id + `"},"state":"live"}`), nil
		}
		return []byte(`{"text":"hello","complete":false,"why":"working"}`), nil
	}}
	data, err := runtime.Operation(context.Background(), LocalOperation{Kind: "read", Address: "t1", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Screen struct {
			Text     string `json:"text"`
			Complete bool   `json:"complete"`
			Why      string `json:"why"`
		} `json:"screen"`
	}
	if json.Unmarshal(data, &result) != nil || result.Screen.Complete || result.Screen.Why != "working" || result.Screen.Text != "hello" {
		t.Fatalf("lost observation semantics: %s", data)
	}
}
