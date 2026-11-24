package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// desktopSessionSeen shares attention over a short resident metadata command,
// independently of the fen attach stream that owns terminal pixels and input.
func (s *daemonServices) desktopSessionSeen(ctx context.Context, raw json.RawMessage) (any, error) {
	var request struct {
		SessionID         string `json:"session_id"`
		ExpectedRuntimeID string `json:"expected_runtime_id"`
		Address           string `json:"address"`
		Turn              uint64 `json:"turn"`
	}
	if json.Unmarshal(raw, &request) != nil || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(request.Address) || !sessionid.Valid(request.SessionID) || request.ExpectedRuntimeID == "" {
		return nil, fmt.Errorf("invalid terminal identity")
	}
	_, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Fennec"}
	}
	s.mu.Lock()
	channel := s.channel
	scope := transferScope{Owner: s.user, RuntimeID: s.config.RuntimeID}
	current := s.epoch == epoch
	s.mu.Unlock()
	if !current || channel == nil || scope.RuntimeID != request.ExpectedRuntimeID {
		return nil, fmt.Errorf("session runtime unavailable")
	}
	channel.mu.Lock()
	service := channel.service
	channel.mu.Unlock()
	if service == nil {
		return nil, fmt.Errorf("session runtime unavailable")
	}
	service.mu.Lock()
	listing := append(json.RawMessage{}, service.listing...)
	generation := service.generation
	service.mu.Unlock()
	var snapshot struct {
		Terminals []struct {
			Address string
			Labels  map[string]string
		}
	}
	if json.Unmarshal(listing, &snapshot) != nil {
		return nil, fmt.Errorf("session unavailable")
	}
	id := ""
	for _, row := range snapshot.Terminals {
		if row.Address == request.Address {
			id = row.Labels["fennec.session"]
			break
		}
	}
	if !sessionid.Valid(id) || id != request.SessionID {
		return nil, fmt.Errorf("session identity required")
	}
	query := func(method string, params any) (json.RawMessage, error) {
		conn, err := s.dialTransfer(ctx, epoch, scope)
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		var output bytes.Buffer
		if err := runHCStream(ctx, conn, []string{"connect"}, bytes.NewReader(append(payload, '\n')), &output, io.Discard); err != nil {
			return nil, err
		}
		var reply struct {
			ID     int
			Result json.RawMessage
			Error  json.RawMessage
		}
		if output.Len() > 4<<20 || json.Unmarshal(output.Bytes(), &reply) != nil || reply.ID != 1 || len(reply.Result) == 0 || len(reply.Error) > 0 && string(reply.Error) != "null" {
			return nil, fmt.Errorf("session update not confirmed")
		}
		return reply.Result, nil
	}
	record, err := query("terminal.get", map[string]any{"terminal": request.Address})
	if err != nil {
		return nil, err
	}
	update, err := needsSeenUpdate(record, id, request.Turn)
	if err != nil {
		return nil, err
	}
	if update {
		if _, err = query("terminal.label", map[string]any{"terminal": request.Address, "labels": map[string]string{"herdr.seen": strconv.FormatUint(request.Turn, 10)}}); err != nil {
			return nil, err
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.generation == generation {
		var value map[string]any
		if json.Unmarshal(service.listing, &value) == nil {
			if rows, ok := value["terminals"].([]any); ok {
				for _, item := range rows {
					row, ok := item.(map[string]any)
					if !ok || row["address"] != request.Address {
						continue
					}
					labels, ok := row["labels"].(map[string]any)
					if !ok {
						continue
					}
					seen, _ := strconv.ParseUint(fmt.Sprint(labels["herdr.seen"]), 10, 64)
					if seen < request.Turn {
						labels["herdr.seen"] = strconv.FormatUint(request.Turn, 10)
					}
				}
			}
			service.listing, _ = json.Marshal(value)
			service.generation++
		}
	}
	return map[string]any{"turn": request.Turn}, nil
}
func needsSeenUpdate(raw json.RawMessage, id string, turn uint64) (bool, error) {
	var record struct {
		State  string
		Turn   uint64
		Labels map[string]string
	}
	if json.Unmarshal(raw, &record) != nil || record.State != "live" || record.Labels["fennec.session"] != id || turn > record.Turn {
		return false, fmt.Errorf("session changed")
	}
	seen, err := strconv.ParseUint(record.Labels["herdr.seen"], 10, 64)
	return err != nil || seen < turn, nil
}
