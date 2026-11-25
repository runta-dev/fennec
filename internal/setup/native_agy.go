package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Antigravity CLI (agy) runs only on its own Google sign-in: Magpie would
// serve it by reverse-proxying Google's Gemini service, which can get the
// account suspended (THI-123). Fennec copies the Mac's sign-in to the cloud.
//
// agy keeps the sign-in through go-keyring: on macOS the Keychain item
// service "gemini", account "antigravity", holding
// "go-keyring-base64:<base64 of the token JSON>"; without a keyring (the
// cloud has no D-Bus session) the same JSON in
// ~/.gemini/antigravity-cli/antigravity-oauth-token:
//
//	{"token":{"access_token","token_type","refresh_token","expiry"},"auth_method":"consumer"}
//
// agy has no variable for its configuration directory, so the cloud copy is
// placed in the cloud's own ~/.gemini right before agy starts.
const (
	agyAuthFile        = "agy/antigravity-oauth-token"
	agyKeychainService = "gemini"
	agyKeychainAccount = "antigravity"
	agyKeyringPrefix   = "go-keyring-base64:"
)

// agyTokenPath is where agy itself reads its sign-in under home.
func agyTokenPath(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}

// agyToken returns the sign-in agy keeps as plain JSON with only the token
// and its auth method, or false when raw holds no sign-in.
func agyToken(raw []byte) ([]byte, bool) {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, agyKeyringPrefix) {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, agyKeyringPrefix))
		if err != nil {
			return nil, false
		}
		value = strings.TrimSpace(string(decoded))
	}
	var source struct {
		Token      map[string]any `json:"token"`
		AuthMethod string         `json:"auth_method"`
	}
	if json.Unmarshal([]byte(value), &source) != nil || source.Token == nil {
		return nil, false
	}
	token := map[string]any{}
	for _, key := range []string{"access_token", "token_type", "refresh_token", "expiry"} {
		if v, ok := source.Token[key].(string); ok && v != "" {
			token[key] = v
		}
	}
	if token["access_token"] == nil && token["refresh_token"] == nil {
		return nil, false
	}
	out := map[string]any{"token": token}
	if source.AuthMethod != "" {
		out["auth_method"] = source.AuthMethod
	}
	data, err := json.Marshal(out)
	return data, err == nil
}

// agyCredentials reads the Mac's sign-in the way agy does: the Keychain item
// first, then the file. nil means agy is not signed in on this Mac.
func (s NativeSource) agyCredentials(ctx context.Context) ([]byte, error) {
	if s.Keychain != nil {
		raw, err := s.Keychain(ctx, agyKeychainService, agyKeychainAccount)
		if err == nil {
			if token, ok := agyToken(raw); ok {
				return token, nil
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	raw, err := readNativeFile(agyTokenPath(s.Home))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if token, ok := agyToken(raw); ok {
		return token, nil
	}
	return nil, nil
}

func agyEntry(v map[string]any) nativeOAuthEntry {
	token, _ := v["token"].(map[string]any)
	entry := nativeOAuthEntry{access: stringValue(token["access_token"]), refresh: stringValue(token["refresh_token"])}
	if expiry, err := time.Parse(time.RFC3339Nano, stringValue(token["expiry"])); err == nil {
		entry.expires = expiry.UnixMilli()
		entry.fresh = entry.expires
	}
	return entry
}

func agyAccounts(read func(string) []byte) []NativeAccountIdentity {
	if _, ok := agyToken(read(agyAuthFile)); !ok {
		return []NativeAccountIdentity{}
	}
	return []NativeAccountIdentity{{Provider: "google", AuthType: "oauth", Subscription: "Antigravity"}}
}

// installAgyToken puts the copy synced from the Mac where agy reads it. A
// sign-in agy already refreshed on the cloud (same refresh token, later
// expiry) stays; a different sign-in from the Mac replaces the cloud's.
func installAgyToken(home, root string) error {
	synced, err := readNativeFile(filepath.Join(root, filepath.FromSlash(agyAuthFile)))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	token, ok := agyToken(synced)
	if !ok {
		return nil
	}
	path := agyTokenPath(home)
	if current, err := readNativeFile(path); err == nil {
		if local, ok := agyToken(current); ok {
			was, next := agyEntry(jsonObject(local)), agyEntry(jsonObject(token))
			if was.refresh != "" && was.refresh == next.refresh && was.fresh >= next.fresh {
				return nil
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ensureNativeDirectory(home, ".gemini/antigravity-cli"); err != nil {
		return err
	}
	return atomicCredential(path, token)
}
