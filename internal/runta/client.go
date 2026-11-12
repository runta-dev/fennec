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

const (
	SharedRuntimeVCPUs             = 2
	SharedRuntimeBaselineMemoryMiB = 4096
	SharedRuntimeMaxMemoryMiB      = 16384
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
	Environment map[string]string `json:"environment_variables"`
}
type RuntimeResources struct {
	Current struct {
		Memory          uint64 `json:"memory_mib"`
		ObservedDiskGiB uint32 `json:"observed_disk_gib"`
	} `json:"current"`
	Requests struct {
		Memory  uint64 `json:"memory_mib"`
		VCPUs   uint32 `json:"vcpus"`
		DiskGiB uint32 `json:"disk_gib"`
	} `json:"requests"`
	Limits struct {
		Memory uint64 `json:"memory_mib"`
	} `json:"limits"`
}

type Client struct {
	SharedAllowed   func(owner string) error
	Endpoint, Token string
	HTTP            *http.Client
	PollInterval    time.Duration
}
type APIError struct{ Status int }

func (e *APIError) Error() string { return fmt.Sprintf("Runta HTTP %d", e.Status) }
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
		return &APIError{response.StatusCode}
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
	for _, r := range runtimes {
		if r.Owner == user && r.Environment["FENNEC_PURPOSE"] == "shared-v1" {
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
		return matches[0], nil
	}
	body := map[string]any{
		"name":                  "Fennec",
		"environment_variables": map[string]string{"FENNEC_PURPOSE": "shared-v1"},
		"resources": map[string]any{
			"requests": map[string]any{"vcpus": SharedRuntimeVCPUs, "memory_mib": SharedRuntimeBaselineMemoryMiB},
			"limits":   map[string]any{"memory_mib": SharedRuntimeMaxMemoryMiB},
		},
	}
	var response struct {
		Data Runtime `json:"data"`
	}
	encodedIntent, err := json.Marshal(body)
	if err != nil {
		return Runtime{}, err
	}
	digest := sha256.Sum256(append([]byte("fennec/shared-v1/"+user+"\x00"), encodedIntent...))
	err = c.request(ctx, "POST", "/v2/runtimes", body, "fennec-"+hex.EncodeToString(digest[:]), &response)
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Status == http.StatusConflict {
		// Another device may have created the shared runtime since our list.
		// Reconcile by ownership, never by changing keys and issuing another POST.
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
	}
	if err == nil && response.Data.ID != "" && response.Data.Owner == user && response.Data.Environment == nil {
		// Environment fields are optional in the API envelope. Verify an omitted
		// marker through readback rather than treating creation as fully verified.
		response.Data, err = c.Get(ctx, response.Data.ID)
	}
	if err == nil && (response.Data.ID == "" || response.Data.Owner != user || response.Data.Environment["FENNEC_PURPOSE"] != "shared-v1") {
		return Runtime{}, fmt.Errorf("runtime ownership or purpose response mismatch")
	}
	return response.Data, err
}
func (c *Client) WaitReady(ctx context.Context, id string, emit func(Runtime)) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	interval := c.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
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
			return fmt.Errorf("runtime %s", r.Status)
		case "shutdown", "stopped":
			return fmt.Errorf("runtime stopped; explicit start required")
		case "paused", "suspended":
			return fmt.Errorf("runtime paused; explicit resume required")
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
