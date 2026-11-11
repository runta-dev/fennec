package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"fennec/internal/cloudstate"
	"fennec/internal/control"
	"fennec/internal/controlplane"
)

type cloudEpochKey struct{}
type cloudJob struct {
	Epoch                          uint64
	Owner, Runtime, Request, Stage string
}
type cloudControl struct {
	actions    sync.Mutex
	mu         sync.Mutex
	job        *cloudJob
	flow       *cloudstate.Service
	services   *daemonServices
	controller *controlplane.Controller
	backend    func(string) cloudstate.Backend
}

func installCloudControl(home string, s *daemonServices, c *controlplane.Controller, backend func(string) cloudstate.Backend) *cloudControl {
	h := &cloudControl{services: s, controller: c, backend: backend}
	h.flow = &cloudstate.Service{Store: cloudstate.New(home), Build: control.BuildID(), Quiesce: func(ctx context.Context, owner, runtimeID string) error {
		epoch, ok := ctx.Value(cloudEpochKey{}).(uint64)
		if !ok {
			return fmt.Errorf("cloud operation epoch required")
		}
		if err := s.validateCloudIdentity(ctx, owner, runtimeID, epoch); err != nil {
			return err
		}
		if err := c.QuiesceCloud(ctx, epoch, runtimeID); err != nil {
			return err
		}
		return s.quiesceCloud(ctx, owner, runtimeID, epoch)
	}}
	h.flow.ValidateLocal = func(ctx context.Context, owner, runtimeID string) error {
		epoch, ok := ctx.Value(cloudEpochKey{}).(uint64)
		if !ok {
			return fmt.Errorf("cloud operation epoch required")
		}
		return s.validateCloudIdentity(ctx, owner, runtimeID, epoch)
	}
	c.Additional["cloud.status"] = h.status
	c.Additional["cloud.delete.preview"] = h.preview
	c.Additional["cloud.delete"] = h.delete
	c.Additional["cloud.create"] = h.create
	return h
}
func (h *cloudControl) session() (context.Context, cloudstate.Backend, uint64, error) {
	lifecycle, token, epoch, ok := h.controller.Account.Session()
	if !ok {
		return nil, nil, 0, &control.Fault{Code: "login_required", Message: "Sign in to manage your cloud computer"}
	}
	return lifecycle, h.backend(token), epoch, nil
}
func (h *cloudControl) status(ctx context.Context, _ json.RawMessage) (any, error) {
	lifecycle, api, epoch, err := h.session()
	if err != nil {
		return nil, err
	}
	h.services.mu.Lock()
	owner, runtimeID, current := h.services.user, h.services.config.RuntimeID, h.services.epoch
	h.services.mu.Unlock()
	if owner == "" || current != epoch {
		owner, err = api.CurrentUser(ctx)
		runtimeID = ""
		if err != nil {
			return nil, err
		}
	}
	record, err := h.flow.Store.Read(owner)
	if err != nil {
		return nil, err
	}
	stage := "available"
	request := ""
	if record.Request != "" {
		runtimeID = record.Runtime
		request = record.Request
		stage = "unconfirmed"
		if record.Rejected {
			stage = "review_required"
		}
		if record.Deleted {
			stage = "deleted"
		}
	}
	h.mu.Lock()
	if j := h.job; j != nil && j.Epoch == epoch && j.Owner == owner {
		stage = j.Stage
		runtimeID = j.Runtime
		request = j.Request
	}
	h.mu.Unlock()
	if lifecycle.Err() != nil {
		return nil, fmt.Errorf("cloud account changed")
	}
	if stage == "deleted" && !record.Deleted {
		stage = "deleting"
	}
	return map[string]any{"stage": stage, "runtime_id": runtimeID, "request_id": request, "can_create": record.Deleted && stage == "deleted"}, nil
}
func (h *cloudControl) preview(ctx context.Context, raw json.RawMessage) (any, error) {
	h.actions.Lock()
	defer h.actions.Unlock()
	var request struct {
		Runtime string `json:"runtime_id"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Runtime == "" {
		return nil, fmt.Errorf("cloud computer required")
	}
	lifecycle, api, _, err := h.session()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	busy := h.job != nil && h.job.Stage == "deleting"
	h.mu.Unlock()
	if busy {
		return nil, &control.Fault{Code: "busy", Message: "Cloud deletion is in progress"}
	}
	plan, err := h.flow.Preview(ctx, api, request.Runtime)
	if lifecycle.Err() != nil {
		return nil, fmt.Errorf("cloud account changed")
	}
	return plan, err
}
func (h *cloudControl) delete(ctx context.Context, raw json.RawMessage) (any, error) {
	h.actions.Lock()
	defer h.actions.Unlock()
	var request struct {
		Plan    string `json:"plan_id"`
		Runtime string `json:"confirm_runtime"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Plan == "" || request.Runtime == "" {
		return nil, fmt.Errorf("confirm the cloud deletion preview")
	}
	lifecycle, api, epoch, err := h.session()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	busy := h.job != nil && h.job.Stage == "deleting"
	h.mu.Unlock()
	if busy {
		return nil, &control.Fault{Code: "busy", Message: "Cloud deletion is in progress"}
	}
	plan, err := h.flow.ValidatePreview(request.Plan, request.Runtime)
	if err != nil {
		return nil, err
	}
	owner, err := api.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if owner != plan.Target.Owner || lifecycle.Err() != nil {
		return nil, fmt.Errorf("cloud account changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job := &cloudJob{Epoch: epoch, Owner: owner, Runtime: request.Runtime, Request: plan.Target.Request, Stage: "deleting"}
	h.mu.Lock()
	h.job = job
	h.mu.Unlock()
	go func() {
		work, cancel := context.WithTimeout(context.WithValue(lifecycle, cloudEpochKey{}, epoch), 3*time.Minute)
		defer cancel()
		_, err := h.flow.Delete(work, api, request.Plan, request.Runtime)
		record, readErr := h.flow.Store.Read(owner)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.job == job {
			job.Stage = "deleted"
			if err != nil {
				job.Stage = "unconfirmed"
				if readErr == nil && record.Rejected {
					job.Stage = "review_required"
				}
			}
		}
	}()
	return map[string]any{"accepted": true, "runtime_id": job.Runtime, "request_id": job.Request}, nil
}
func (h *cloudControl) create(ctx context.Context, raw json.RawMessage) (any, error) {
	h.actions.Lock()
	defer h.actions.Unlock()
	var request struct {
		Runtime string `json:"confirm_runtime"`
		Request string `json:"request_id"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Runtime == "" || request.Request == "" {
		return nil, fmt.Errorf("confirm creation after cloud deletion")
	}
	lifecycle, api, epoch, err := h.session()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	busy := h.job != nil && h.job.Stage == "deleting"
	h.mu.Unlock()
	if busy {
		return nil, fmt.Errorf("cloud deletion is still running")
	}
	if err = h.flow.CreateAgain(ctx, api, request.Runtime, request.Request); err != nil {
		return nil, err
	}
	if lifecycle.Err() != nil {
		return nil, fmt.Errorf("cloud account changed")
	}
	if err = h.controller.ResumeCloud(epoch); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.job = nil
	h.mu.Unlock()
	return h.controller.Reconcile(ctx)
}
