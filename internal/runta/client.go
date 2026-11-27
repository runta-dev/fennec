// Package runta implements the Runtime REST API, independently of Cloud Agents.
package runta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default Fennec runtime shape: 2-8 vCPU, 4-16 GiB memory, 100 GiB disk. An
// idle machine holds 2 vCPU and 4 GiB and bursts with its load up to the
// limits. SharedRuntimeMaxVCPUs is sent as limits.vcpus; an API that predates
// dynamic vCPU rejects that field, so creation falls back to a fixed
// fixedSharedRuntimeVCPUs runtime (see sharedCreateIntents), since a fixed
// machine cannot burst.
const (
	SharedRuntimeVCPUs             = 2
	SharedRuntimeMaxVCPUs          = 8
	SharedRuntimeBaselineMemoryMiB = 4096
	SharedRuntimeMaxMemoryMiB      = 16384
	SharedRuntimeDiskGiB           = 100

	// fixedSharedRuntimeVCPUs sizes a runtime on an API without dynamic vCPU,
	// and is the baseline of runtimes created with the earlier 4-8 vCPU
	// default; memory reconciliation still applies to them.
	fixedSharedRuntimeVCPUs = 4
)

type Runtime struct {
	Resources   *RuntimeResources `json:"resources"`
	Secrets     []json.RawMessage `json:"secret_configuration"`
	Revision    uint64            `json:"revision"`
	ID          string            `json:"id"`
	Name        string            `json:"display_name"`
	Owner       string            `json:"owner_user_id"`
	Status      string            `json:"status"`
	Degraded    bool              `json:"degraded"`
	SSHEnabled  bool              `json:"ssh_enabled"`
	ErrorCode   string            `json:"error_code"`
	Environment map[string]string `json:"environment_variables"`
	// TLSPassthroughHosts are tunnelled to the original destination by the
	// egress gateway instead of intercepted.
	TLSPassthroughHosts []string `json:"tls_passthrough_hosts"`
}
type RuntimeResources struct {
	Current struct {
		VCPUs           uint32 `json:"vcpus"`
		Memory          uint64 `json:"memory_mib"`
		ObservedDiskGiB uint32 `json:"observed_disk_gib"`
	} `json:"current"`
	Requests struct {
		Memory  uint64 `json:"memory_mib"`
		VCPUs   uint32 `json:"vcpus"`
		DiskGiB uint32 `json:"disk_gib"`
	} `json:"requests"`
	Limits struct {
		VCPUs  uint32 `json:"vcpus"`
		Memory uint64 `json:"memory_mib"`
	} `json:"limits"`
}

type Client struct {
	SharedAllowed      func(owner string) error
	CreationGeneration func(owner string) string
	// RetireCreation durably advances the generation before a replacement POST.
	RetireCreation  func(owner, generation string) error
	Endpoint, Token string
	HTTP            *http.Client
	PollInterval    time.Duration
	NoSSHNoDelay    bool
	// RetainSSHHome opts the daemon's initial connection into a private local
	// carrier that survives daemon replacement. Recovery can still dial fresh.
	RetainSSHHome string
}

// APIError is a non-2xx answer. Code is the Runta JSON error code ("" when the
// body was not a Runta error envelope, e.g. a proxy or CDN error page).
type APIError struct {
	Status  int
	Code    string
	message string
}

func (e *APIError) retiredCreation() bool {
	if e.Status != http.StatusConflict || e.Code != "already_exists" {
		return false
	}
	// Only this explicit refusal proves that the old key cannot create a
	// machine. Generic conflicts and uncertain outcomes must keep their key.
	message := strings.TrimPrefix(e.message, "runtime already exists: ")
	return message == "idempotency key was already consumed by a deleted runtime" ||
		message == "idempotency key was consumed by a deleted runtime"
}

func (e *APIError) Error() string { return fmt.Sprintf("Runta HTTP %d", e.Status) }

// BillingRestricted reports whether err means the account cannot run a cloud
// computer until it adds credits: a refused create/start (HTTP 402 or a
// billing error code) or a runtime that stopped because the balance ran out.
func BillingRestricted(err error) bool {
	if err == nil {
		return false
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "balance_exhausted", "billing_restricted", "insufficient_balance", "payment_required":
			return true
		}
		return apiError.Status == http.StatusPaymentRequired
	}
	return strings.Contains(err.Error(), "billing_restricted")
}

var errRetiredCreation = errors.New("creation key belongs to a deleted cloud computer")

func (c *Client) request(ctx context.Context, method, path string, body any, key string, out any) error {
	stage := "api.other"
	switch {
	case path == "/v2/me":
		stage = "api.identity"
	case path == "/v2/ssh-keys":
		stage = "api.device_keys"
	case path == "/v2/ssh-host-key":
		stage = "api.host_key"
	case strings.HasSuffix(path, "/ssh-keys"):
		stage = "api.runtime_keys"
	case strings.HasPrefix(path, "/v2/runtimes/"):
		stage = "api.runtime"
	}
	defer launchprofile.Begin(stage)()
	endpoint := strings.TrimRight(c.Endpoint, "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid API endpoint")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return fmt.Errorf("HTTPS required")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiError := &APIError{Status: response.StatusCode}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope) == nil {
			apiError.Code = envelope.Error.Code
			apiError.message = envelope.Error.Message
		}
		return apiError
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
}
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	var response struct {
		Data struct {
			ID string `json:"user_id"`
		} `json:"data"`
	}
	if err := c.request(ctx, "GET", "/v2/me", nil, "", &response); err != nil {
		return "", err
	}
	if response.Data.ID == "" {
		return "", fmt.Errorf("missing user identity")
	}
	return response.Data.ID, nil
}
func (c *Client) Get(ctx context.Context, id string) (Runtime, error) {
	if id == "" {
		return Runtime{}, fmt.Errorf("runtime identity required")
	}
	var response struct {
		Data Runtime `json:"data"`
	}
	err := c.request(ctx, "GET", "/v2/runtimes/"+url.PathEscape(id), nil, "", &response)
	if err == nil && response.Data.ID != id {
		return Runtime{}, fmt.Errorf("runtime response identity mismatch")
	}
	return response.Data, err
}
func (c *Client) List(ctx context.Context) ([]Runtime, error) {
	var all []Runtime
	cursor := ""
	seen := map[string]bool{}
	for {
		path := "/v2/runtimes?limit=100"
		if cursor != "" {
			path += "&after=" + url.QueryEscape(cursor)
		}
		var response struct {
			Data       []Runtime `json:"data"`
			Pagination struct {
				More bool   `json:"has_more"`
				Next string `json:"next_cursor"`
			} `json:"pagination"`
		}
		if err := c.request(ctx, "GET", path, nil, "", &response); err != nil {
			return nil, err
		}
		all = append(all, response.Data...)
		if !response.Pagination.More {
			return all, nil
		}
		cursor = response.Pagination.Next
		if cursor == "" || seen[cursor] {
			return nil, fmt.Errorf("invalid pagination cursor")
		}
		seen[cursor] = true
	}
}

// EnsureShared uses authenticated ownership, a versioned purpose marker and a
// stable create idempotency key. This does not claim permanent cross-device
// uniqueness beyond the server's idempotency retention contract.
func (c *Client) EnsureShared(ctx context.Context) (Runtime, error) {
	type listing struct {
		runtimes []Runtime
		err      error
	}
	listed := make(chan listing, 1)
	go func() { runtimes, err := c.List(ctx); listed <- listing{runtimes, err} }()
	user, err := c.CurrentUser(ctx)
	if err != nil {
		return Runtime{}, err
	}
	if c.SharedAllowed != nil {
		if err := c.SharedAllowed(user); err != nil {
			return Runtime{}, err
		}
	}
	result := <-listed
	if result.err != nil {
		return Runtime{}, result.err
	}
	runtimes := result.runtimes
	var matches []Runtime
	var terminalIDs []string
	for _, r := range runtimes {
		if r.Owner == user && r.Environment["FENNEC_PURPOSE"] == "shared-v1" {
			if r.Status == "deleted" || r.Status == "deleting" || r.Status == "error" || r.Status == "failed" || r.Status == "crashed" {
				if r.ID != "" {
					terminalIDs = append(terminalIDs, r.ID)
				}
				continue
			}
			matches = append(matches, r)
		}
	}
	if len(matches) > 1 {
		return Runtime{}, fmt.Errorf("multiple Fennec runtimes require reconciliation")
	}
	if len(matches) == 1 {
		if matches[0].ID == "" {
			return Runtime{}, fmt.Errorf("Fennec runtime response is missing identity")
		}
		return c.reconcileSharedResources(ctx, matches[0], user), nil
	}
	baseSeed := "fennec/shared-v1/" + user
	seed := baseSeed
	if c.CreationGeneration != nil {
		if gen := strings.TrimSpace(c.CreationGeneration(user)); gen != "" {
			seed += "/after-" + gen
		}
	}
	if len(terminalIDs) > 0 {
		seed += "/skip-" + strings.Join(terminalIDs, ",")
	}
	created, err := c.createShared(ctx, user, seed)
	if errors.Is(err, errRetiredCreation) && c.RetireCreation != nil {
		// createShared has just reconciled the full list and found no owned
		// Fennec runtime. Derive the next generation from the refused seed so
		// concurrent devices converge; persist before POST so a lost response
		// or daemon restart retries the same replacement, not a new machine.
		digest := sha256.Sum256([]byte("retired:" + seed))
		generation := hex.EncodeToString(digest[:])
		if err := c.RetireCreation(user, generation); err != nil {
			return Runtime{}, fmt.Errorf("persist replacement creation identity: %w", err)
		}
		seed = baseSeed + "/after-" + generation
		// One recovery per preparation, never an unbounded creation loop.
		created, err = c.createShared(ctx, user, seed)
	}
	if err == nil && (created.Status == "deleted" || created.Status == "deleting") && created.ID != "" {
		// The server replayed an earlier creation under this key whose runtime
		// has since been deleted; derive a fresh key from that runtime.
		created, err = c.createShared(ctx, user, seed+"/after-"+created.ID)
	}
	if err != nil {
		return Runtime{}, err
	}
	var response struct {
		Data Runtime `json:"data"`
	}
	response.Data = created
	if response.Data.ID != "" && response.Data.Owner == user && response.Data.Environment == nil {
		// Environment fields are optional in the API envelope. Verify an omitted
		// marker through readback rather than treating creation as fully verified.
		response.Data, err = c.Get(ctx, response.Data.ID)
	}
	if err == nil && (response.Data.ID == "" || response.Data.Owner != user || response.Data.Environment["FENNEC_PURPOSE"] != "shared-v1") {
		return Runtime{}, fmt.Errorf("runtime ownership or purpose response mismatch")
	}
	return response.Data, err
}

// createShared POSTs the shared runtime, newest API shape first (see
// sharedCreateIntents). The Idempotency-Key digests seed and body, so retries
// of one logical creation reuse it.
func (c *Client) createShared(ctx context.Context, user, seed string) (Runtime, error) {
	var response struct {
		Data Runtime `json:"data"`
	}
	var err error
	intents := sharedCreateIntents()
	for i, body := range intents {
		var encodedIntent []byte
		if encodedIntent, err = json.Marshal(body); err != nil {
			return Runtime{}, err
		}
		digest := sha256.Sum256(append([]byte(seed+"\x00"), encodedIntent...))
		response.Data = Runtime{}
		err = c.request(ctx, "POST", "/v2/runtimes", body, "fennec-"+hex.EncodeToString(digest[:]), &response)
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Status == http.StatusConflict {
			// Another device may have created the shared runtime since our list.
			// Reconcile by ownership before considering an explicitly retired key.
			current, listErr := c.List(ctx)
			if listErr != nil {
				return Runtime{}, fmt.Errorf("runtime creation conflict; reconciliation failed: %w", listErr)
			}
			var candidate *Runtime
			for _, r := range current {
				if r.Owner != user || r.Environment["FENNEC_PURPOSE"] != "shared-v1" {
					continue
				}
				if r.ID == "" || candidate != nil {
					return Runtime{}, fmt.Errorf("runtime creation conflict; ambiguous Fennec runtime identity")
				}
				copy := r
				candidate = &copy
			}
			if candidate != nil {
				return *candidate, nil
			}
			if apiError.retiredCreation() {
				return Runtime{}, errors.Join(errRetiredCreation, err)
			}
		}
		// A validation rejection means the API predates a requested field
		// (limits.vcpus for dynamic vCPU, then disk_gib); a rejected request
		// creates nothing, so retry with the next older shape.
		if errors.As(err, &apiError) && (apiError.Status == http.StatusBadRequest || apiError.Status == http.StatusUnprocessableEntity) && i+1 < len(intents) {
			continue
		}
		if err != nil {
			return Runtime{}, err
		}
		break
	}
	return response.Data, nil
}

// sharedCreateIntents lists Fennec runtime creation bodies, newest API shape
// first: dynamic 2-8 vCPU, then fixed 4 vCPU for an API without limits.vcpus,
// then without disk_gib for an API that cannot size the overlay. Each shape has
// its own idempotency key because the key digests the body.
func sharedCreateIntents() []map[string]any {
	shape := func(dynamicVCPU, disk bool) map[string]any {
		vcpus := fixedSharedRuntimeVCPUs
		if dynamicVCPU {
			vcpus = SharedRuntimeVCPUs
		}
		requests := map[string]any{"vcpus": vcpus, "memory_mib": SharedRuntimeBaselineMemoryMiB}
		if disk {
			requests["disk_gib"] = SharedRuntimeDiskGiB
		}
		limits := map[string]any{"memory_mib": SharedRuntimeMaxMemoryMiB}
		if dynamicVCPU {
			limits["vcpus"] = SharedRuntimeMaxVCPUs
		}
		return map[string]any{
			"name":                  "Fennec",
			"environment_variables": map[string]string{"FENNEC_PURPOSE": "shared-v1"},
			"resources":             map[string]any{"requests": requests, "limits": limits},
		}
	}
	return []map[string]any{shape(true, true), shape(false, true), shape(false, false)}
}

// RuntimeGone reports whether runtime id, owned by owner, was permanently
// deleted. It must never mistake a temporary condition for a deletion:
// production deploys may pause machines briefly, and the Mac's network may be
// down, so a lost connection says nothing about existence.
//
// Deletion requires two independent authoritative answers from the API:
//  1. GET /v2/runtimes/{id} returns a Runta 404 with error code "not_found",
//     or status deleted/deleting; and
//  2. the same token still belongs to owner, and owner's complete runtime
//     listing has no live entry for id.
//
// Any running, paused, stopped, suspended, provisioning or failed machine
// exists (false, nil). Every other outcome is unknown and returned as an error:
// timeouts, 5xx, 401/403, a bare 404 without the Runta error envelope (a
// CDN/proxy page or a route missing mid-deploy), an identity change, or a
// listing that still shows the machine.
func (c *Client) RuntimeGone(ctx context.Context, owner, id string) (bool, error) {
	if owner == "" || id == "" {
		return false, fmt.Errorf("runtime owner and identity required")
	}
	r, err := c.Get(ctx, id)
	var apiError *APIError
	switch {
	case errors.As(err, &apiError) && apiError.Status == http.StatusNotFound:
		if apiError.Code != "not_found" {
			return false, err
		}
	case err != nil:
		return false, err
	case r.Status != "deleted" && r.Status != "deleting":
		return false, nil
	}
	current, err := c.CurrentUser(ctx)
	if err != nil {
		return false, err
	}
	if current != owner {
		return false, fmt.Errorf("runtime owner changed")
	}
	runtimes, err := c.List(ctx)
	if err != nil {
		return false, err
	}
	for _, listed := range runtimes {
		if listed.ID == id && listed.Status != "deleted" && listed.Status != "deleting" {
			return false, fmt.Errorf("runtime %s still listed as %q", id, listed.Status)
		}
	}
	return true, nil
}
func (c *Client) WaitReady(ctx context.Context, id string, emit func(Runtime)) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	started := time.Now()
	for {
		r, err := c.Get(ctx, id)
		if err != nil {
			return err
		}
		if emit != nil {
			emit(r)
		}
		switch r.Status {
		case "running":
			if !r.Degraded && r.SSHEnabled {
				return nil
			}
		case "failed", "error", "crashed", "unavailable", "deleted", "deleting":
			if r.ErrorCode == "balance_exhausted" || r.ErrorCode == "billing_restricted" {
				return fmt.Errorf("billing_restricted: %s", r.ErrorCode)
			}
			if r.ErrorCode != "" {
				return fmt.Errorf("runtime %s (%s)", r.Status, r.ErrorCode)
			}
			return fmt.Errorf("runtime %s", r.Status)
		case "shutdown", "stopped":
			return fmt.Errorf("runtime stopped; explicit start required")
		case "paused", "suspended":
			return fmt.Errorf("runtime paused; explicit resume required")
		}
		interval := c.PollInterval
		if interval <= 0 {
			if time.Since(started) < 12*time.Second {
				interval = 300 * time.Millisecond
			} else {
				interval = time.Second
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (c *Client) Start(ctx context.Context, id string) error {
	r, err := c.Get(ctx, id)
	if err != nil {
		return err
	}
	return c.request(ctx, "POST", fmt.Sprintf("/v2/runtimes/%s/start?expected_revision=%d", url.PathEscape(id), r.Revision), nil, "", nil)
}
func (c *Client) Resume(ctx context.Context, id string) error {
	r, err := c.Get(ctx, id)
	if err != nil {
		return err
	}
	return c.request(ctx, "POST", fmt.Sprintf("/v2/runtimes/%s/resume?expected_revision=%d", url.PathEscape(id), r.Revision), nil, "", nil)
}

// reconcileSharedResources patches an existing Fennec runtime (for example a
// legacy fixed 2c4g or fixed 2c16g machine) to 4 GiB baseline / 16 GiB max
// dynamic memory scaling so idle memory automatically drops back to 4 GiB.
// vCPU and disk are left alone: changing them needs a restart or full
// shutdown, so existing runtimes get the 2-8 vCPU / 100 GiB shape on recreate.
func (c *Client) reconcileSharedResources(ctx context.Context, r Runtime, expectedOwner string) Runtime {
	if r.ID == "" || r.Resources == nil || r.Status == "deleting" || r.Status == "deleted" {
		return r
	}
	if v := r.Resources.Requests.VCPUs; v != SharedRuntimeVCPUs && v != fixedSharedRuntimeVCPUs {
		return r
	}
	if r.Resources.Requests.Memory == SharedRuntimeBaselineMemoryMiB && r.Resources.Limits.Memory == SharedRuntimeMaxMemoryMiB {
		return r
	}
	rev := r.Revision
	if rev == 0 {
		fresh, err := c.Get(ctx, r.ID)
		if err != nil || fresh.Revision == 0 {
			return r
		}
		r = fresh
		rev = fresh.Revision
		if r.Resources != nil && r.Resources.Requests.Memory == SharedRuntimeBaselineMemoryMiB && r.Resources.Limits.Memory == SharedRuntimeMaxMemoryMiB {
			return r
		}
	}
	patchBody := map[string]any{
		"resources": map[string]any{
			"requests": map[string]any{"memory_mib": SharedRuntimeBaselineMemoryMiB},
			"limits":   map[string]any{"memory_mib": SharedRuntimeMaxMemoryMiB},
		},
	}
	var updated struct {
		Data Runtime `json:"data"`
	}
	err := c.request(ctx, http.MethodPatch, fmt.Sprintf("/v2/runtimes/%s?expected_revision=%d", url.PathEscape(r.ID), rev), patchBody, "", &updated)
	if err != nil {
		return r
	}
	if updated.Data.ID == r.ID && updated.Data.Owner == expectedOwner {
		if updated.Data.Environment == nil {
			updated.Data.Environment = r.Environment
		}
		return updated.Data
	}
	return r
}
