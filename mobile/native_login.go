package fennecmobile

import (
	"errors"
	"fennec/internal/account"
)

// NativeLogin uses public keys from the matching Runta deployment configuration.
// These keys identify the Supabase project; they are not service credentials.
func (a *Auth) NativeLogin(provider string) (string, error) {
	config, err := a.nativeConfig()
	if err != nil {
		return "", err
	}
	state, err := a.manager.LoginNative(provider, config)
	return snapshotJSON(state), err
}
func (a *Auth) EmailLogin(email string) (string, error) {
	config, err := a.nativeConfig()
	if err != nil {
		return "", err
	}
	state, err := a.manager.LoginEmail(email, config)
	return snapshotJSON(state), err
}
func (a *Auth) VerifyEmail(code string, epoch int64) bool {
	return epoch > 0 && a.manager.VerifyEmail(code, uint64(epoch))
}
func (a *Auth) nativeConfig() (account.NativeOAuthConfig, error) {
	var config account.NativeOAuthConfig
	switch a.environment.API {
	case "https://api.runta.com":
		config = account.NativeOAuthConfig{AuthURL: "https://auth.runta.com", BackendURL: "https://backend.runta.com", PublishableKey: "sb_publishable_bUrzVB8cSWjNlEM_G4pnCA_BVa0xbm2"}
	case "https://api.runta.xyz":
		config = account.NativeOAuthConfig{AuthURL: "https://auth.runta.xyz", BackendURL: "https://backend.runta.xyz", PublishableKey: "sb_publishable_Nr9eGltgOAIbrt_TwKfmrA_ZXHyBfHq"}
	default:
		return config, errors.New("native sign-in is unavailable for this environment")
	}
	return config, nil
}
func DeepLinkRoute(raw string) string { return account.DeepLinkRoute(raw) }
func (a *Auth) CompleteNativeLogin(raw string, epoch int64) bool {
	return epoch > 0 && a.manager.CompleteNative(raw, uint64(epoch))
}
