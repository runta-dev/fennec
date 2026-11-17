package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Pairing is a sign-in code for another device, approved by this signed-in
// client. URL carries only the short-lived device code (in the fragment, so it
// never reaches a server log); the other device redeems it at
// /v2/auth/device/token for a token of its own.
type Pairing struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Pairing errors are deliberately distinct from login errors: a refusal here
// means the approve endpoint does not accept this client, not that the Mac's
// own sign-in expired, so it must never sign the Mac out.
var (
	ErrPairingNotAllowed     = errors.New("pairing_not_allowed")
	ErrPairingOrganization   = errors.New("pairing_organization_required")
	errPairingInvalidPayload = errors.New("pairing_failed")
)

const pairingScheme = "fennec://pair"

// PairDevice starts a device authorization for deviceName and approves it with
// this account, the same two calls the Runta dashboard's /device page makes.
func (m *Manager) PairDevice(parent context.Context, deviceName string) (Pairing, error) {
	ctx, token, epoch, done, err := m.queryContext(parent)
	if err != nil {
		return Pairing{}, err
	}
	defer done()
	endpoint := m.Endpoint()

	var authorization struct {
		Data struct {
			DeviceCode string    `json:"device_code"`
			URL        string    `json:"verification_uri_complete"`
			ExpiresAt  time.Time `json:"expires_at"`
		} `json:"data"`
	}
	status, err := m.post(ctx, "authorization", map[string]string{"client_id": "runta_crew", "device_name": deviceName, "app_url": m.DashboardURL()}, &authorization)
	if err != nil || status < 200 || status >= 300 || authorization.Data.DeviceCode == "" {
		return Pairing{}, errPairingInvalidPayload
	}
	link, err := url.Parse(authorization.Data.URL)
	code := ""
	if err == nil {
		code = link.Query().Get("code")
	}
	if code == "" {
		return Pairing{}, errPairingInvalidPayload
	}

	var lookup struct {
		Data struct {
			Organizations []struct {
				ID string `json:"id"`
			} `json:"organizations"`
		} `json:"data"`
	}
	if err := m.pairingRequest(ctx, token, http.MethodGet, endpoint+"/v1/auth/device/authorization?code="+url.QueryEscape(code), nil, &lookup); err != nil {
		return Pairing{}, err
	}
	organization, err := m.pairingOrganization(ctx, token, endpoint, epoch, lookup.Data.Organizations)
	if err != nil {
		return Pairing{}, err
	}
	if err := m.pairingRequest(ctx, token, http.MethodPost, endpoint+"/v1/auth/device/authorization/approve", map[string]string{"code": code, "organization_id": organization}, nil); err != nil {
		return Pairing{}, err
	}

	expiry := authorization.Data.ExpiresAt
	if limit := time.Now().Add(10 * time.Minute); expiry.IsZero() || expiry.After(limit) {
		expiry = limit
	}
	return Pairing{URL: PairingURL(authorization.Data.DeviceCode, endpoint), ExpiresAt: expiry}, nil
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

func (m *Manager) pairingOrganization(ctx context.Context, token, endpoint string, epoch uint64, organizations []struct {
	ID string `json:"id"`
}) (string, error) {
	if len(organizations) == 1 && organizations[0].ID != "" {
		return organizations[0].ID, nil
	}
	if len(organizations) == 0 {
		return "", ErrPairingOrganization
	}
	// Several organizations: approve for the account's current one, as the
	// dashboard would preselect it.
	me, err := m.me(ctx, token, endpoint, epoch)
	if err == nil {
		data, _ := me["data"].(map[string]any)
		if data == nil {
			data = me
		}
		if current, _ := data["organization_id"].(string); current != "" {
			for _, organization := range organizations {
				if organization.ID == current {
					return current, nil
				}
			}
		}
	}
	return "", ErrPairingOrganization
}

// pairingRequest calls the dashboard device-authorization API with this
// client's token. 401 and 403 are reported as ErrPairingNotAllowed and do not
// invalidate the stored credential.
func (m *Manager) pairingRequest(ctx context.Context, token, method, address string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrPairingNotAllowed
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &httpStatusError{status: response.StatusCode}
	}
	if out == nil {
		return nil
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out) != nil {
		return errPairingInvalidPayload
	}
	return nil
}
