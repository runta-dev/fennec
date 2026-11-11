package cloudstate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"fennec/internal/runta"
)

type Backend interface {
	CurrentUser(context.Context) (string, error)
	Get(context.Context, string) (runta.Runtime, error)
	DeleteOwnedShared(context.Context, string, string, uint64) error
}
type Plan struct {
	ID      string    `json:"id"`
	Target  Record    `json:"target"`
	Name    string    `json:"name"`
	Build   string    `json:"build"`
	Expires time.Time `json:"expires"`
}

// Service is shared by CLI and UI. Quiesce must stop preparation and resident
// services, with account/runtime fencing, without deleting local login or files.
type Service struct {
	Store         *Store
	Build         string
	Quiesce       func(context.Context, string, string) error
	ValidateLocal func(context.Context, string, string) error
	mu            sync.Mutex
	plans         map[string]Plan
}

func (s *Service) Preview(ctx context.Context, api Backend, id string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || s.Build == "" || s.Store == nil {
		return Plan{}, fmt.Errorf("cloud deletion target required")
	}
	owner, err := api.CurrentUser(ctx)
	if err != nil {
		return Plan{}, err
	}
	pending, err := s.Store.Read(owner)
	if err != nil {
		return Plan{}, err
	}
	if pending.Request != "" && pending.Runtime != id {
		return Plan{}, fmt.Errorf("resolve the original cloud deletion first")
	}
	runtime, err := api.Get(ctx, id)
	runtimeMissing := err != nil
	var missing *runta.APIError
	if err != nil && !(pending.Request != "" && errors.As(err, &missing) && missing.Status == http.StatusNotFound) {
		return Plan{}, err
	}
	if err == nil && (runtime.Owner != owner || runtime.ID != id || runtime.Environment["FENNEC_PURPOSE"] != "shared-v1") {
		return Plan{}, fmt.Errorf("cloud computer ownership changed")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return Plan{}, err
	}
	plan := Plan{ID: hex.EncodeToString(nonce[:]), Name: runtime.Name, Build: s.Build, Expires: time.Now().Add(5 * time.Minute), Target: pending}
	if pending.Request == "" || pending.Rejected {
		plan.Target = Record{Owner: owner, Runtime: id, Request: plan.ID, Revision: runtime.Revision}
		if pending.Rejected && (runtimeMissing || runtime.Status == "deleting" || runtime.Status == "deleted") {
			plan.Target.Revision = pending.Revision
		}
	}
	if s.plans == nil {
		s.plans = map[string]Plan{}
	}
	for key, old := range s.plans {
		if !time.Now().Before(old.Expires) {
			delete(s.plans, key)
		}
	}
	if len(s.plans) >= 32 {
		return Plan{}, fmt.Errorf("too many pending cloud previews")
	}
	s.plans[plan.ID] = plan
	return plan, nil
}
func (s *Service) Delete(ctx context.Context, api Backend, planID, confirmedRuntime string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok || plan.Build != s.Build || !time.Now().Before(plan.Expires) || confirmedRuntime != plan.Target.Runtime {
		return Record{}, fmt.Errorf("review and confirm the current cloud deletion preview")
	}
	if s.Quiesce == nil {
		return Record{}, fmt.Errorf("cloud service shutdown is unavailable")
	}
	owner, err := api.CurrentUser(ctx)
	if err != nil {
		return Record{}, err
	}
	if owner != plan.Target.Owner {
		return Record{}, runta.ErrDeletionAccountChanged
	}
	if err = ctx.Err(); err != nil {
		return Record{}, err
	}
	target := plan.Target
	// Revalidate before disrupting local services. DeleteOwnedShared repeats
	// this check at the destructive boundary to cover changes after this read.
	runtime, err := api.Get(ctx, target.Runtime)
	var missing *runta.APIError
	if err != nil && !(errors.As(err, &missing) && missing.Status == http.StatusNotFound) {
		return Record{}, err
	}
	if err == nil {
		if runtime.ID != target.Runtime || runtime.Owner != target.Owner || runtime.Environment["FENNEC_PURPOSE"] != "shared-v1" {
			return Record{}, fmt.Errorf("cloud computer ownership changed")
		}
		if runtime.Revision != target.Revision && runtime.Status != "deleting" && runtime.Status != "deleted" {
			return Record{}, fmt.Errorf("cloud computer changed; review a new deletion preview")
		}
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	// Persist before quiescing or contacting DELETE, retaining exact revision on
	// explicit recovery. A lost reply cannot authorize replacement provisioning.
	if s.ValidateLocal != nil {
		if err := s.ValidateLocal(ctx, owner, target.Runtime); err != nil {
			return Record{}, err
		}
	}
	pending := target
	pending.Deleted = false
	if err = s.Store.Begin(pending); err != nil {
		return Record{}, err
	}
	if err = s.Quiesce(ctx, owner, target.Runtime); err != nil {
		return Record{}, err
	}
	current, err := s.Store.Read(owner)
	if err != nil {
		return Record{}, err
	}
	if !current.Deleted {
		if err = api.DeleteOwnedShared(ctx, target.Runtime, owner, target.Revision); err != nil {
			if errors.Is(err, runta.ErrDeletionRejected) {
				err = errors.Join(err, s.Store.Reject(owner, target.Runtime, target.Request))
			}
			return Record{}, err
		}
		if err = s.Store.Confirm(owner, target.Runtime, target.Request); err != nil {
			return Record{}, err
		}
	}
	delete(s.plans, planID)
	return s.Store.Read(owner)
}

// ValidatePreview allows the transport to reject stale confirmation before
// acknowledging an asynchronous deletion job. Delete validates again at execution.
func (s *Service) ValidatePreview(planID, runtime string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok || plan.Build != s.Build || !time.Now().Before(plan.Expires) || plan.Target.Runtime != runtime {
		return Plan{}, fmt.Errorf("review and confirm the current cloud deletion preview")
	}
	return plan, nil
}
func (s *Service) CreateAgain(ctx context.Context, api Backend, confirmedRuntime, request string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := api.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err := s.Store.AllowCreation(owner, confirmedRuntime, request); err != nil {
		return err
	}
	for id, plan := range s.plans {
		if plan.Target.Owner == owner {
			delete(s.plans, id)
		}
	}
	return nil
}
