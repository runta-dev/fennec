package runta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// TailscaleTLSPassthroughHosts are the hosts the cloud computer's tailscaled
// talks to: the control server (controlplane.tailscale.com) and the DERP
// relays (derpN.tailscale.com). The Runta egress gateway terminates TLS and
// passes only WebSocket upgrades, so Tailscale's `tailscale-control-protocol`
// and `DERP` upgrades get a bare 400 (FEN-104). With these hosts in the
// runtime's TLS passthrough list the gateway tunnels the connection to the
// original destination instead. Nothing is lost by not intercepting: both
// protocols are end-to-end encrypted (Noise and NaCl boxes) inside TLS.
var TailscaleTLSPassthroughHosts = []string{"*.tailscale.com"}

// EnsureTLSPassthrough adds hosts to runtime id's TLS passthrough list,
// keeping the entries it already has. It reports whether it changed the list.
// A concurrent update is retried once against the fresh revision.
func (c *Client) EnsureTLSPassthrough(ctx context.Context, id string, hosts []string) (bool, error) {
	for attempt := 0; ; attempt++ {
		r, err := c.Get(ctx, id)
		if err != nil {
			return false, err
		}
		merged := slices.Clone(r.TLSPassthroughHosts)
		for _, host := range hosts {
			if !slices.ContainsFunc(merged, func(have string) bool { return strings.EqualFold(have, host) }) {
				merged = append(merged, host)
			}
		}
		if len(merged) == len(r.TLSPassthroughHosts) {
			return false, nil
		}
		if r.Revision == 0 {
			return false, fmt.Errorf("runtime revision unknown")
		}
		var updated struct {
			Data Runtime `json:"data"`
		}
		path := fmt.Sprintf("/v2/runtimes/%s?expected_revision=%d", url.PathEscape(id), r.Revision)
		err = c.request(ctx, http.MethodPatch, path, map[string]any{"tls_passthrough_hosts": merged}, "", &updated)
		var apiError *APIError
		if attempt == 0 && errors.As(err, &apiError) && (apiError.Status == http.StatusConflict || apiError.Status == http.StatusPreconditionFailed) {
			continue
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
}
