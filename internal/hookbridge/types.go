// Package hookbridge defines session-scoped hook transport and local execution.
// It knows neither cmux nor a terminal application's private API.
package hookbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const PayloadLimit = 65536
const ApprovalTimeout = 2 * time.Minute
const MaxExecution = 2 * time.Minute
const MaxCallDuration = ApprovalTimeout + MaxExecution + 5*time.Second

type Policy string

const (
	Required Policy = "required"
	Observer Policy = "observer"
)

type Binding struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Event     string `json:"event"`
	Policy    Policy `json:"policy"`
	TimeoutMS int    `json:"timeout_ms"`
}

var referencePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (b Binding) Validate() error {
	if !referencePattern.MatchString(b.ID) || b.Name == "" || len(b.Name) > 128 || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`).MatchString(b.Event) || (b.Policy != Required && b.Policy != Observer) || b.TimeoutMS < 1 || b.TimeoutMS > int(MaxExecution/time.Millisecond) {
		return fmt.Errorf("invalid hook binding")
	}
	return nil
}
func (b Binding) Wait() time.Duration {
	return ApprovalTimeout + time.Duration(b.TimeoutMS)*time.Millisecond + time.Second
}
func ValidateBindings(bindings []Binding) error {
	if len(bindings) > 64 {
		return fmt.Errorf("too many hook bindings")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if err := b.Validate(); err != nil {
			return err
		}
		if seen[b.ID] {
			return fmt.Errorf("duplicate hook binding")
		}
		seen[b.ID] = true
	}
	return nil
}

type Request struct {
	BindingID string `json:"binding_id"`
	Input     []byte `json:"input"`
}
type Result struct {
	Stdout []byte `json:"stdout,omitempty"`
	Stderr []byte `json:"stderr,omitempty"`
	Code   int    `json:"code"`
	Status string `json:"status"`
}

func (r Result) Valid() bool {
	valid := r.Status == "executed" || (r.Status == "skipped" && r.Code == 0) || ((r.Status == "blocked" || r.Status == "unregistered") && r.Code == 2)
	return valid && r.Code >= 0 && r.Code <= 255 && len(r.Stdout)+len(r.Stderr) <= PayloadLimit
}
func Failure(b Binding, reason string) Result {
	if b.Policy == Observer {
		return Result{Stdout: []byte("{}\n"), Status: "skipped"}
	}
	return Result{Code: 2, Stderr: []byte("Fennec: required local hook " + reason + ". Retry after restoring access.\n"), Status: "blocked"}
}
func Unregistered() Result {
	result := Failure(Binding{Policy: Required}, "is not registered")
	result.Status = "unregistered"
	return result
}
func Normalize(binding Binding, result Result) Result {
	if !result.Valid() {
		return Failure(binding, "returned an invalid response")
	}
	if result.Status == "unregistered" {
		return Failure(binding, "configuration is unavailable")
	}
	if binding.Policy == Required && result.Status == "skipped" {
		return Failure(binding, "was not executed")
	}
	return result
}
func CodexExit(binding Binding, result Result) int {
	if binding.Policy == Required && result.Code != 0 {
		return 2
	}
	return result.Code
}

type Call struct {
	RuntimeID string `json:"runtime_id"`
	SessionID string `json:"session_id"`
	LeaseID   string `json:"lease_id"`
	BindingID string `json:"binding_id"`
	Input     []byte `json:"input"`
}

// Executor descriptions and implementations are registered locally. Requests
// from the guest contain only a reference and stdin, never a command or policy.
type Executor interface {
	Binding() Binding
	Fingerprint() (string, error)
	Description() string
	Execute(context.Context, []byte) Result
}
type Authorize func(context.Context, string, string) (context.Context, func(), error)

func Execute(ctx context.Context, owner, runtimeID string, executor Executor, input []byte, authorize Authorize) Result {
	binding := executor.Binding()
	if binding.Validate() != nil || len(input) > PayloadLimit || owner == "" || runtimeID == "" || authorize == nil {
		return Failure(binding, "has no valid authorization context")
	}
	if ctx.Err() != nil {
		return Failure(binding, "was cancelled")
	}
	before, err := executor.Fingerprint()
	if err != nil || before != binding.ID {
		return Failure(binding, "configuration changed")
	}
	scopeData, _ := json.Marshal([]string{"hook-v1", owner, runtimeID, before})
	scope := "hook:" + Digest(scopeData)
	prompt, cancelPrompt := context.WithTimeout(ctx, ApprovalTimeout)
	grant, release, err := authorize(prompt, executor.Description(), scope)
	// An authorization lease is independent of the prompt deadline, but remains
	// tied to the caller/account and trust revocation until release.
	cancelPrompt()
	if err != nil || grant == nil || release == nil {
		return Failure(binding, "was not approved")
	}
	defer release()
	if ctx.Err() != nil || grant.Err() != nil {
		return Failure(binding, "authorization was revoked")
	}
	after, err := executor.Fingerprint()
	if err != nil || after != before {
		return Failure(binding, "configuration changed during approval")
	}
	run, cancel := context.WithTimeout(ctx, time.Duration(binding.TimeoutMS)*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(grant, cancel)
	defer stop()
	result := executor.Execute(run, input)
	if run.Err() != nil {
		return Failure(binding, "timed out or lost authorization")
	}
	return Normalize(binding, result)
}
