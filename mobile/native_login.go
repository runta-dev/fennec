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
func (a *Auth) EmailLogin(email, turnstileToken string) (string, error) {
	config, err := a.nativeConfig()
	if err != nil {
		return "", err
	}
	state, err := a.manager.LoginEmail(email, turnstileToken, config)
	return snapshotJSON(state), err
}

// AppleLogin starts Sign in with Apple; the app shows Apple's sheet and then
// calls CompleteAppleLogin with its identity token.
func (a *Auth) AppleLogin() (string, error) {
	config, err := a.nativeConfig()
	if err != nil {
		return "", err
	}
	state, err := a.manager.LoginApple(config)
	return snapshotJSON(state), err
}

// CompleteAppleLogin takes the Apple identity token, the raw nonce whose
// SHA-256 was in the Apple request, and the name Apple shared (or "").
func (a *Auth) CompleteAppleLogin(idToken, nonce, fullName string, epoch int64) bool {
	return epoch > 0 && a.manager.CompleteApple(idToken, nonce, fullName, uint64(epoch))
}
func (a *Auth) VerifyEmail(code string, epoch int64) bool {
	return epoch > 0 && a.manager.VerifyEmail(code, uint64(epoch))
}
func (a *Auth) nativeConfig() (account.NativeOAuthConfig, error) {
	var config account.NativeOAuthConfig
	switch a.environment.API {
	case "https://api.runta.com":
		config = account.NativeOAuthConfig{AuthURL: "https://auth.runta.com", BackendURL: "https://backend.runta.com", DashboardURL: "https://dashboard.runta.com", PublishableKey: "sb_publishable_bUrzVB8cSWjNlEM_G4pnCA_BVa0xbm2"}
	case "https://api.runta.xyz":
		config = account.NativeOAuthConfig{AuthURL: "https://auth.runta.xyz", BackendURL: "https://backend.runta.xyz", DashboardURL: "https://dashboard.runta.xyz", PublishableKey: "sb_publishable_Nr9eGltgOAIbrt_TwKfmrA_ZXHyBfHq"}
	default:
		return config, errors.New("native sign-in is unavailable for this environment")
	}
	config.KeyName = a.keyName
	return config, nil
}
func DeepLinkRoute(raw string) string { return account.DeepLinkRoute(raw) }
func (a *Auth) CompleteNativeLogin(raw string, epoch int64) bool {
	return epoch > 0 && a.manager.CompleteNative(raw, uint64(epoch))
}
