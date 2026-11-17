package account

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Pairing signs in another device without copying this client's token. The
// Mac starts a device authorization for the iPhone; the QR code (URL) carries
// only the short-lived device code, in the fragment so it never reaches a
// server log; the user approves ApproveURL on the Runta dashboard, where the
// browser is normally signed in; the iPhone redeems the code for a token of
// its own (Manager.Redeem).
type Pairing struct {
	URL        string    `json:"url"`
	ApproveURL string    `json:"approve_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// ErrPairingRateLimited reports Runta's per-network limit on new device
// authorizations (10 per 15 minutes). Runta currently returns it as HTTP 500.
var ErrPairingRateLimited = errors.New("pairing_rate_limited")

const pairingScheme = "fennec://pair"

// PairDevice starts a device authorization named deviceName. It needs no
// credential, but the Mac must be signed in so the code belongs to its
// environment and the dashboard approval matches the account in use.
func (m *Manager) PairDevice(parent context.Context, deviceName string) (Pairing, error) {
	ctx, _, _, done, err := m.queryContext(parent)
	if err != nil {
		return Pairing{}, err
	}
	defer done()
	var authorization struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			DeviceCode string    `json:"device_code"`
			URL        string    `json:"verification_uri_complete"`
			ExpiresAt  time.Time `json:"expires_at"`
		} `json:"data"`
	}
	dashboard := m.DashboardURL()
	status, err := m.post(ctx, "authorization", map[string]string{"client_id": "runta_crew", "device_name": deviceName, "app_url": dashboard}, &authorization)
	if err != nil && status == 0 {
		return Pairing{}, fmt.Errorf("start authorization: %w", err)
	}
	// Runta maps its rate limit to 500 "internal" (identity_auth_error has no
	// ResourceExhausted case); treat both forms as the limit.
	if status == 429 || (status == 500 && authorization.Error.Code == "internal") {
		return Pairing{}, fmt.Errorf("start authorization: HTTP %d: %w", status, ErrPairingRateLimited)
	}
	if status < 200 || status >= 300 || authorization.Data.DeviceCode == "" {
		return Pairing{}, fmt.Errorf("start authorization: HTTP %d (%s: %s)", status, authorization.Error.Code, authorization.Error.Message)
	}
	approve, err := url.Parse(authorization.Data.URL)
	want, _ := url.Parse(dashboard)
	if err != nil || approve.Scheme != "https" || want == nil || approve.Host != want.Host || approve.User != nil {
		return Pairing{}, fmt.Errorf("start authorization: unexpected approval link")
	}
	expiry := authorization.Data.ExpiresAt
	if limit := time.Now().Add(15 * time.Minute); expiry.IsZero() || expiry.After(limit) {
		expiry = limit
	}
	return Pairing{URL: PairingURL(authorization.Data.DeviceCode, m.Endpoint()), ApproveURL: approve.String(), ExpiresAt: expiry}, nil
}

// PairingURL encodes a device code and the Runta API origin it belongs to, so
// the scanning device signs in to the same environment.
func PairingURL(deviceCode, endpoint string) string {
	fragment := url.Values{"code": {deviceCode}, "api": {strings.TrimRight(endpoint, "/")}}
	return pairingScheme + "#" + fragment.Encode()
}

// ParsePairingURL is the inverse of PairingURL.
func ParsePairingURL(raw string) (deviceCode, endpoint string, err error) {
	prefix := pairingScheme + "#"
	if !strings.HasPrefix(raw, prefix) {
		return "", "", fmt.Errorf("not a Fennec pairing code")
	}
	values, err := url.ParseQuery(strings.TrimPrefix(raw, prefix))
	if err != nil || values.Get("code") == "" || values.Get("api") == "" {
		return "", "", fmt.Errorf("not a Fennec pairing code")
	}
	return values.Get("code"), values.Get("api"), nil
}
