package runtime

import (
	"context"
	"encoding/json"
	"fennec/internal/agylog"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ValidSessionID reports whether id names a Fennec session.
func ValidSessionID(id string) bool {
	return sessionIDPattern.MatchString(id)
}

// Read returns a session's screen. With paint, a live terminal's screen comes
// as the terminal draws it, colors included: a thread shows it.
func (r *LocalRuntime) Read(ctx context.Context, address, id string, paint bool) (json.RawMessage, error) {
	terminal, err := r.Terminal(ctx, address, id)
	if err != nil {
		return nil, err
	}
	if paint && terminal.State == "live" && r.Observe != nil {
		if paint, err := r.Observe(ctx, address); err == nil && len(paint) > 0 {
			return json.Marshal(map[string]any{"terminal": terminal, "vt": paint, "cols": terminal.Cols, "rows": terminal.Rows})
		}
	}
	raw, err := r.Run(ctx, "read", address, "--json", "--lines", "0")
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 || !json.Valid(raw) {
		return nil, fmt.Errorf("invalid terminal screen")
	}
	return json.Marshal(map[string]any{"terminal": terminal, "screen": json.RawMessage(raw)})
}

// Send types text into a live session once; an unknown outcome is reported,
// never retried.
func (r *LocalRuntime) Send(ctx context.Context, address, id, text string) (json.RawMessage, error) {
	lookupStarted := time.Now()
	terminal, err := r.Terminal(ctx, address, id)
	lookupDuration := time.Since(lookupStarted)
	if err != nil {
		return nil, err
	}
	if terminal.State != "live" {
		return nil, fmt.Errorf("terminal is not live")
	}
	sendStarted := time.Now()
	raw, err := r.Run(ctx, "send", "--json", "--", address, text)
	sendDuration := time.Since(sendStarted)
	if err != nil {
		return nil, fmt.Errorf("input outcome unknown; inspect before retrying")
	}
	if len(raw) > 1<<20 || !json.Valid(raw) {
		return nil, fmt.Errorf("input result invalid; inspect before retrying")
	}
	return json.Marshal(map[string]any{"receipt": json.RawMessage(raw), "timings_ms": map[string]float64{
		"identity_check": float64(lookupDuration.Microseconds()) / 1000,
		"herdr_send":     float64(sendDuration.Microseconds()) / 1000,
	}})
}

// List is herdr's session listing reduced to its public fields. The cloud's
// account and gateway state are added by cloudops.List.
func (r *LocalRuntime) List(ctx context.Context) (json.RawMessage, error) {
	// One live snapshot contains the labels, title and status of every row.
	raw, err := r.Query(ctx, "terminal.list", map[string]any{})
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
	for i, detail := range rows {
		// Keep the existing session surface small. In particular, a complete
		// Herdr record includes argv, which can contain the agent's prompt.
		row := map[string]any{}
		for _, key := range []string{"address", "name", "template", "parent", "state", "turn", "exit_code", "signal", "reason", "cwd", "title", "labels", "status", "status_ms", "created_ms", "finished_ms"} {
			if value, ok := detail[key]; ok {
				row[key] = value
			}
		}
		if status, ok := detail["status"].(map[string]any); ok {
			row["agent"] = status["app"]
		}
		if row["agent"] == nil || row["agent"] == "" {
			if argv, ok := detail["argv"].([]any); ok && len(argv) > 0 {
				if program, ok := argv[0].(string); ok {
					row["agent"] = filepath.Base(program)
				}
			}
		}
		if labels, ok := detail["labels"].(map[string]any); ok {
			if where, ok := labels["where"].(string); ok && where != "" {
				row["where"] = where
			}
		}
		// agy never sets a terminal title; it names the conversation in its
		// own index instead, so every client shows that name (FEN-72).
		if title, _ := row["title"].(string); strings.TrimSpace(title) == "" && isAgy(row) {
			if cwd, ok := row["cwd"].(string); ok {
				if named, err := agylog.Title(r.Home, cwd); err == nil && named != "" {
					row["title"] = named
				}
			}
		}
		rows[i] = row
	}
	listing["terminals"], err = json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	// Match ls's public machine summary, excluding connection configuration.
	if raw, ok := listing["machines"]; ok {
		var machines []map[string]json.RawMessage
		if json.Unmarshal(raw, &machines) != nil {
			return nil, fmt.Errorf("invalid machine rows")
		}
		for i, machine := range machines {
			public := map[string]json.RawMessage{}
			for _, key := range []string{"name", "state", "since_ms", "why"} {
				if value, ok := machine[key]; ok {
					public[key] = value
				}
			}
			machines[i] = public
		}
		listing["machines"], _ = json.Marshal(machines)
	}
	return json.Marshal(listing)
}

// isAgy reports whether a terminal row is an Antigravity CLI session.
func isAgy(row map[string]any) bool {
	if labels, ok := row["labels"].(map[string]any); ok && labels["fennec.agent"] == "agy" {
		return true
	}
	return row["agent"] == "antigravity" || row["agent"] == "agy"
}
