package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"io"
)

// SessionParams contains client-controlled fields only. Runtime identity and
// authorization always come from daemon-owned account/runtime state.
type SessionParams struct {
	ExpectedRuntimeID string   `json:"expected_runtime_id,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	Agent             string   `json:"agent,omitempty"`
	Directory         string   `json:"directory,omitempty"`
	Address           string   `json:"address,omitempty"`
	Text              string   `json:"text,omitempty"`
	Arguments         []string `json:"arguments,omitempty"`
}

func (c *Controller) sessionHandler(operation string) control.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params SessionParams
		if len(raw) > 0 && string(raw) != "null" {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&params) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Invalid session parameters"}
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return nil, &control.Fault{Code: "invalid_params", Message: "Invalid session parameters"}
			}
		}
		if operation != "list" && operation != "refresh" && params.SessionID == "" {
			return nil, &control.Fault{Code: "invalid_params", Message: "Session identity required"}
		}
		lifecycle, token, epoch, ok := c.Account.Session()
		if !ok {
			return nil, &control.Fault{Code: "login_required", Message: "Sign in before accessing sessions"}
		}
		runtime := c.runtimeSnapshot()
		if runtime.Epoch != epoch || runtime.RuntimeID == "" {
			return nil, &control.Fault{Code: "runtime_unavailable", Message: "Prepare the shared runtime first", Retryable: true}
		}
		if params.ExpectedRuntimeID != "" && params.ExpectedRuntimeID != runtime.RuntimeID {
			return nil, &control.Fault{Code: "runtime_mismatch", Message: "Session runtime mismatch"}
		}
		operationCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(lifecycle, cancel)
		defer stop()
		return c.Session(operationCtx, token, runtime.RuntimeID, operation, params)
	}
}
