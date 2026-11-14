package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// Use the public API through connect so Herdr resolves its own socket and
// configuration. The CLI's ls formatter drops labels and titles, requiring an
// extra get per terminal; terminal.list returns one complete, current snapshot.
func herdrQuery(home string) func(context.Context, string, any) (json.RawMessage, error) {
	return func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		step, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		command := exec.CommandContext(step, filepath.Join(home, ".local/bin/herdr-core"), "connect")
		command.Stdin = bytes.NewReader(append(request, '\n'))
		raw, err := command.Output()
		if err != nil {
			return nil, err
		}
		return decodeHerdrReply(raw)
	}
}

func decodeHerdrReply(raw []byte) (json.RawMessage, error) {
	var reply struct {
		Version string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if len(raw) > (4<<20)+65536 || json.Unmarshal(raw, &reply) != nil || reply.Version != "2.0" || reply.ID != 1 || len(reply.Error) > 0 && string(reply.Error) != "null" || len(reply.Result) == 0 || string(reply.Result) == "null" {
		return nil, fmt.Errorf("Herdr query was not confirmed")
	}
	return reply.Result, nil
}
