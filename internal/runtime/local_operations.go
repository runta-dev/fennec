package runtime

import (
	"context"
	"encoding/json"
	"fennec/internal/setup"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type LocalOperation struct {
	Configuration *setup.RuntimeConfiguration `json:"configuration,omitempty"`
	Login         *setup.NativeLogin          `json:"login,omitempty"`
	NativeModel   *setup.NativeModelSelection `json:"native_model,omitempty"`
	Accounts      *setup.ModelAccountsRequest `json:"accounts,omitempty"`
	Routes        []setup.AgentRoute          `json:"routes,omitempty"`
	Launch        *LocalLaunch                `json:"launch,omitempty"`
	Kind          string                      `json:"kind"`
	Address       string                      `json:"address,omitempty"`
	SessionID     string                      `json:"session_id,omitempty"`
	Text          string                      `json:"text,omitempty"`
}

func (o LocalOperation) Validate() error {
	if o.Kind == "reconfigure_runtime" {
		if o.Configuration == nil {
			return fmt.Errorf("configuration required")
		}
		return o.Configuration.Validate()
	}
	if o.Kind == "native_login_status" || o.Kind == "native_login_cancel" {
		if o.Login == nil || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(o.Address) {
			return fmt.Errorf("login required")
		}
		return o.Login.Validate()
	}
	if o.Kind == "native_models" || o.Kind == "native_model_set" || o.Kind == "native_account" {
		if o.NativeModel == nil || !map[string]bool{"codex": true, "claude": true, "pi": true}[o.NativeModel.Agent] {
			return fmt.Errorf("unsupported native model agent")
		}
		if o.Kind == "native_model_set" && !setup.NativeModelFamily(o.NativeModel.Agent, o.NativeModel.Model) {
			return fmt.Errorf("unsupported native model")
		}
		return nil
	}

	if o.Kind == "model_catalog" {
		return nil
	}
	if o.Kind == "model_accounts" && o.Accounts != nil {
		return setup.ValidateModelAccountsRequest(*o.Accounts)
	}
	if o.Kind == "reconcile_routes" {
		return setup.ValidateDesiredRoutes(o.Routes)
	}
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
	if o.Kind == "reconfigure_runtime" {
		result, err := setup.ReconfigureRuntime(ctx, r.Home, *o.Configuration)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	if o.Kind == "native_account" {
		view, err := setup.NativeCloudAccount(r.Home, o.NativeModel.Agent)
		if err != nil {
			return nil, err
		}
		return json.Marshal(view)
	}
	if o.Kind == "native_login_status" || o.Kind == "native_login_cancel" {
		terminal, err := r.Terminal(ctx, o.Address, o.Login.ID)
		if err != nil {
			return nil, err
		}
		if o.Kind == "native_login_cancel" {
			if err := setup.CancelNativeLogin(ctx, r.Home, *o.Login); err != nil {
				root, selected, readErr := setup.NativeAccountRoot(r.Home, o.Login.Agent)
				if readErr == nil && selected && filepath.Base(root) == o.Login.ID {
					return json.Marshal(map[string]bool{"signed_in": true})
				}
				return nil, err
			}
			if _, err := r.Stop(ctx, o.Address, o.Login.ID); err != nil {
				return nil, err
			}
			if err := setup.DiscardNativeLogin(ctx, r.Home, *o.Login); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]bool{"canceled": true})
		}
		if terminal.State != "live" && terminal.State != "finished" {
			return nil, fmt.Errorf("login terminal state unavailable")
		}
		root, selected, err := setup.NativeAccountRoot(r.Home, o.Login.Agent)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"signed_in": selected && filepath.Base(root) == o.Login.ID, "running": terminal.State == "live", "agent": o.Login.Agent, "session_id": o.Login.ID})
	}
	if o.Kind == "native_models" || o.Kind == "native_model_set" {
		var state setup.NativeModelState
		var err error
		if !setup.NativeAgentMode(r.Home) {
			return nil, fmt.Errorf("native models unavailable in gateway mode")
		}
		if o.Kind == "native_models" {
			state, err = setup.NativeModels(ctx, r.Home, o.NativeModel.Agent, true)
		} else {
			state, err = setup.SetNativeModel(ctx, r.Home, *o.NativeModel)
		}
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	}
	if o.Kind == "model_catalog" {
		models, err := setup.LocalGatewayModels(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(models)
	}
	if o.Kind == "model_accounts" {
		result, err := setup.ApplyModelAccounts(ctx, r.Home, *o.Accounts)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	if o.Kind == "reconcile_routes" {
		results, err := setup.ReconcileLocalRoutes(ctx, r.Home, o.Routes)
		if err != nil {
			return nil, err
		}
		return json.Marshal(results)
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
	lookupStarted := time.Now()
	terminal, err := r.Terminal(ctx, o.Address, o.SessionID)
	lookupDuration := time.Since(lookupStarted)
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
	sendStarted := time.Now()
	raw, err := r.Run(ctx, "send", "--json", "--", o.Address, o.Text)
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
	if accounts, e := setup.SelectedNativeLogins(r.Home); e == nil && len(accounts) > 0 {
		listing["native_accounts"], _ = json.Marshal(accounts)
	}
	return json.Marshal(listing)
}
