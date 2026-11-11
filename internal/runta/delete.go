package runta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// DeleteOwnedShared removes only the exact runtime approved in a fresh preview.
// A successful DELETE response is acceptance, not proof of deletion. Never
// retry a destructive request with a new revision or resolve a replacement ID.
func (c *Client) DeleteOwnedShared(ctx context.Context, id, owner string, revision uint64) error {
	if id == "" || owner == "" {
		return fmt.Errorf("confirmed runtime identity required")
	}
	user, err := c.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if user != owner {
		return fmt.Errorf("account changed; review the uninstall request again")
	}
	runtime, err := c.Get(ctx, id)
	if isRuntimeMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if runtime.Owner != owner || runtime.Environment["FENNEC_PURPOSE"] != "shared-v1" {
		return fmt.Errorf("runtime ownership or Fennec identity changed")
	}
	if runtime.Status == "deleted" {
		return nil
	}
	if runtime.Revision != revision && runtime.Status != "deleting" {
		return fmt.Errorf("cloud computer changed; review the uninstall request again")
	}
	var deletionErr error
	if runtime.Status != "deleting" {
		deletionErr = c.request(ctx, http.MethodDelete, fmt.Sprintf("/v2/runtimes/%s?expected_revision=%d", url.PathEscape(id), revision), nil, "", nil)
		var rejected *APIError
		if errors.As(deletionErr, &rejected) && rejected.Status >= 400 && rejected.Status < 500 && rejected.Status != http.StatusNotFound {
			return deletionErr
		}
		// Even a lost response may have committed. Read the same ID before deciding;
		// never replay DELETE or treat an authorization failure as proof of absence.
	}
	interval := c.PollInterval
	if interval == 0 {
		interval = time.Second
	}
	for {
		current, err := c.Get(ctx, id)
		if isRuntimeMissing(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cloud deletion is not confirmed: %w", err)
		}
		if current.Owner != owner || current.Environment["FENNEC_PURPOSE"] != "shared-v1" {
			return fmt.Errorf("cloud identity changed while verifying deletion")
		}
		if current.Status == "deleted" {
			return nil
		}
		if deletionErr != nil && current.Status != "deleting" {
			return fmt.Errorf("cloud deletion is not confirmed: %w", deletionErr)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("cloud deletion is not confirmed: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
func isRuntimeMissing(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}
