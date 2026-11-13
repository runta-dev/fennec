//go:build chromiumlive

package chromium

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fennec/cua/internal/chromeimport"
)

// TestLiveKeychainMigration runs the bundled Chromium (FENNEC_CUA_CHROMIUM)
// headless on a temporary profile against a temporary keychain (a
// throwaway HOME): a cookie set under the mock keychain is carried to the
// Keychain key, Chromium creates its Safe Storage item in the temporary
// keychain only, and the stored value no longer opens with the mock key.
// The user's Keychain is never touched.
func TestLiveKeychainMigration(t *testing.T) {
	bin := Executable(os.Getenv(EnvApp))
	if st, err := os.Stat(bin); bin == "" || err != nil || st.IsDir() {
		t.Skip("set FENNEC_CUA_CHROMIUM to a Chromium.app")
	}
	service := UpstreamSafeStorageService
	if s, ok := SafeStorageOf(bin); ok {
		service = s.Service
	}
	k, err := NewTempKeychain(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := k.Close(); err != nil {
			t.Error(err)
		}
	}()
	profile := filepath.Join(t.TempDir(), "profile")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := func(mock bool, fn func(b *Browser)) {
		b := New(Options{Bin: bin, ProfileDir: profile, Headless: true, MockKeychain: mock, SafeStorageService: service,
			Env: k.Env, IdleTimeout: -1, Log: t.Logf})
		defer b.Close()
		fn(b)
	}
	const name, value = "fennec_migration", "kept-value"
	run(true, func(b *Browser) {
		c, _, err := b.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Call(ctx, "", "Storage.setCookies", map[string]any{"cookies": []map[string]any{{
			"name": name, "value": value, "domain": ".example.test", "path": "/", "secure": true,
			"expires": float64(time.Now().Add(24 * time.Hour).Unix())}}}); err != nil {
			t.Fatal(err)
		}
	})
	if s, _ := k.Services(); len(s) != 0 {
		t.Fatalf("the mock keychain wrote %v", s)
	}
	cookie := func(b *Browser) string {
		c, _, err := b.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := c.Call(ctx, "", "Storage.getCookies", nil)
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Cookies []struct{ Name, Value string } `json:"cookies"`
		}
		_ = json.Unmarshal(raw, &r)
		for _, ck := range r.Cookies {
			if ck.Name == name {
				return ck.Value
			}
		}
		return ""
	}
	run(false, func(b *Browser) {
		if v := cookie(b); v != value {
			t.Errorf("after the migration the cookie is %q", v)
		}
	})
	st, err := ReadCookieState(profile)
	if err != nil || st == nil || st.Store != "keychain" || st.Migration == nil || st.Migration.Carried != 1 {
		t.Fatalf("state %+v %v", st, err)
	}
	s, err := k.Services()
	if err != nil || !slices.Contains(s, service) {
		t.Fatalf("keychain items %v %v; want %q", s, err, service)
	}
	if service == SafeStorageService && slices.Contains(s, UpstreamSafeStorageService) {
		t.Errorf("the renamed build also created %q", UpstreamSafeStorageService)
	}
	// Again: the value now decrypts with the Keychain key only.
	run(false, func(b *Browser) {
		if v := cookie(b); v != value {
			t.Errorf("after a restart the cookie is %q", v)
		}
	})
	cs, stats, err := chromeimport.ReadCookies(ctx, filepath.Join(profile, "Default"), chromeimport.StaticKey(mockPassword), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			t.Error("the stored cookie still opens with the mock key")
		}
	}
	if stats.Undecryptable == 0 {
		t.Errorf("no value is encrypted with another key: %+v", stats)
	}
	t.Logf("Keychain items in the temporary keychain: %v; migration %+v", s, *st.Migration)
}
