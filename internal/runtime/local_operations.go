package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

type LocalOperation struct {
	Launch    *LocalLaunch `json:"launch,omitempty"`
	Kind      string       `json:"kind"`
	Address   string       `json:"address,omitempty"`
	SessionID string       `json:"session_id,omitempty"`
	Text      string       `json:"text,omitempty"`
}

func (o LocalOperation) Validate() error {
	if o.Kind == "launch" && o.Launch != nil {
		return o.Launch.Validate()
	}
	if o.Kind == "list" {
		return nil
	}
	if o.Kind != "read" && o.Kind != "send" && o.Kind != "stop" {
		return fmt.Errorf("unsupported operation")
	}
	if !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(o.Address) || !sessionIDPattern.MatchString(o.SessionID) {
		return fmt.Errorf("invalid session identity")
	}
	if o.Kind == "send" && (strings.TrimSpace(o.Text) == "" || len(o.Text) > 16384 || strings.ContainsRune(o.Text, 0)) {
		return fmt.Errorf("input must contain 1–16384 bytes without NUL")
	}
	return nil
}
func (r *LocalRuntime) Operation(ctx context.Context, o LocalOperation) (json.RawMessage, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Kind == "list" {
		return r.List(ctx)
	}
	if o.Kind == "launch" {
		terminal, err := r.Launch(ctx, *o.Launch)
		if err != nil {
			return nil, err
		}
		return json.Marshal(terminal)
	}
	if o.Kind == "stop" {
		terminal, err := r.Stop(ctx, o.Address, o.SessionID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(terminal)
	}
	terminal, err := r.Terminal(ctx, o.Address, o.SessionID)
	if err != nil {
		return nil, err
	}
	if o.Kind == "read" {
		raw, err := r.Run(ctx, "read", o.Address, "--json", "--lines", "0")
		if err != nil {
			return nil, err
		}
		if len(raw) > 1<<20 || !json.Valid(raw) {
			return nil, fmt.Errorf("invalid terminal screen")
		}
		return json.Marshal(map[string]any{"terminal": terminal, "screen": json.RawMessage(raw)})
	}
	if terminal.State != "live" {
		return nil, fmt.Errorf("terminal is not live")
	}
	raw, err := r.Run(ctx, "send", "--json", "--", o.Address, o.Text)
	if err != nil {
		return nil, fmt.Errorf("input outcome unknown; inspect before retrying")
	}
	if len(raw) > 1<<20 || !json.Valid(raw) {
		return nil, fmt.Errorf("input result invalid; inspect before retrying")
	}
	return json.Marshal(map[string]any{"receipt": json.RawMessage(raw)})
}
func (r *LocalRuntime) List(ctx context.Context) (json.RawMessage, error) {
	// Session surfaces show live programs. Enumerating every historical terminal
	// added a separate local get process for each completed test or old session.
	raw, err := r.Run(ctx, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var listing map[string]json.RawMessage
	if len(raw) > 4<<20 || json.Unmarshal(raw, &listing) != nil {
		return nil, fmt.Errorf("invalid session list")
	}
	var rows []map[string]any
	if json.Unmarshal(listing["terminals"], &rows) != nil {
		return nil, fmt.Errorf("invalid terminal rows")
	}
	if len(rows) > 4096 {
		return nil, fmt.Errorf("too many terminal rows")
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				row := rows[index]
				address, _ := row["address"].(string)
				if !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(address) {
					continue
				}
				data, err := r.Run(ctx, "get", address)
				if err != nil {
					continue
				}
				var detail map[string]any
				if len(data) > 1<<20 || json.Unmarshal(data, &detail) != nil || detail["address"] != address {
					continue
				}
				for _, key := range []string{"title", "name", "parent", "labels", "status", "status_ms", "created_ms", "finished_ms", "state", "exit_code"} {
					if value, ok := detail[key]; ok {
						row[key] = value
					}
				}
			}
		}()
	}
	for i := range rows {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	listing["terminals"], err = json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	return json.Marshal(listing)
}
