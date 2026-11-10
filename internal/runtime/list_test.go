package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestSessionListEnrichesTitlesAndRetainsFailedRows(t *testing.T) {
	runtime := &LocalRuntime{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[{"address":"t1","state":"live"},{"address":"t2","state":"live"}]}`), nil
		}
		if args[1] == "t1" {
			return []byte(`{"address":"t1","title":"Fix login redirect","status":"working","labels":{"fennec.session":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		}
		return nil, fmt.Errorf("get failed")
	}}
	output, err := runtime.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Terminals []map[string]any `json:"terminals"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Terminals) != 2 {
		t.Fatal("lost fallback row")
	}
	if result.Terminals[0]["title"] != "Fix login redirect" || result.Terminals[0]["status"] != "working" {
		t.Fatal(result)
	}
	if result.Terminals[1]["address"] != "t2" || result.Terminals[1]["state"] != "live" {
		t.Fatal(result)
	}
}
