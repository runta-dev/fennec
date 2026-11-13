package chromium

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/chromeimport"
)

func TestSafeStorageOf(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Chromium.app")
	bin := filepath.Join(app, "Contents", "MacOS", "Chromium")
	res := filepath.Join(app, "Contents", "Resources")
	if err := os.MkdirAll(res, 0o755); err != nil {
		t.Fatal(err)
	}
	if AppDir(bin) != app {
		t.Fatalf("AppDir %s", AppDir(bin))
	}
	if _, ok := SafeStorageOf(bin); ok {
		t.Error("a build without the marker counts as renamed")
	}
	for body, want := range map[string]bool{
		`{"service":"Fennec Safe Storage","account":"Chromium"}`:   true,
		`{"service":"Chromium Safe Storage","account":"Chromium"}`: false,
		`not json`: false,
	} {
		_ = os.WriteFile(filepath.Join(res, SafeStorageMarker), []byte(body), 0o644)
		if s, ok := SafeStorageOf(bin); ok != want || ok && s.Service != SafeStorageService {
			t.Errorf("%s: %v %v", body, s, ok)
		}
	}
}

func TestArgsKeychain(t *testing.T) {
	if strings.Contains(strings.Join(Args(Options{ProfileDir: "/p"}), " "), "--use-mock-keychain") {
		t.Error("the Keychain build runs with the mock keychain")
	}
	if !strings.Contains(strings.Join(Args(Options{ProfileDir: "/p", MockKeychain: true}), " "), "--use-mock-keychain") {
		t.Error("MockKeychain is not passed")
	}
}

// fakeCookieDB makes a profile with a cookie database (its content is
// read by the stubbed reader).
func fakeCookieDB(t *testing.T, profile string) string {
	dir := filepath.Join(profile, "Default", "Network")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Cookies", "Cookies-journal"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("db "+n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func stubMockReader(t *testing.T, cookies []chromeimport.Cookie, st chromeimport.Stats, err error) {
	old := readMockCookies
	readMockCookies = func(context.Context, string) ([]chromeimport.Cookie, chromeimport.Stats, error) {
		return append([]chromeimport.Cookie(nil), cookies...), st, err
	}
	t.Cleanup(func() { readMockCookies = old })
}

func TestPrepareCookieStore(t *testing.T) {
	ctx := context.Background()
	nolog := func(string, ...any) {}
	keychain := Options{SafeStorageService: SafeStorageService}

	t.Run("fresh profile", func(t *testing.T) {
		o := keychain
		o.ProfileDir = t.TempDir()
		p, err := prepareCookieStore(ctx, o, nolog)
		if err != nil || p != nil {
			t.Fatalf("%v %v", p, err)
		}
		if st, _ := ReadCookieState(o.ProfileDir); st == nil || st.Store != "keychain" || st.Service != SafeStorageService || st.Migration != nil {
			t.Errorf("state %+v", st)
		}
	})

	t.Run("mock then keychain refused", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := prepareCookieStore(ctx, Options{ProfileDir: dir, MockKeychain: true}, nolog); err != nil {
			t.Fatal(err)
		}
		if st, _ := ReadCookieState(dir); st == nil || st.Store != "mock" {
			t.Fatalf("state %+v", st)
		}
		_ = writeCookieState(dir, CookieState{Store: "keychain"})
		if _, err := prepareCookieStore(ctx, Options{ProfileDir: dir, MockKeychain: true}, nolog); !errors.Is(err, ErrKeychainProfile) {
			t.Errorf("a Keychain profile started on the mock keychain: %v", err)
		}
	})

	t.Run("migration sets aside and restores", func(t *testing.T) {
		o := keychain
		o.ProfileDir = t.TempDir()
		db := fakeCookieDB(t, o.ProfileDir)
		stubMockReader(t, []chromeimport.Cookie{{Name: "sid", Value: "v", Domain: ".example.test", Path: "/"}}, chromeimport.Stats{Found: 2, Expired: 1}, nil)
		p, err := prepareCookieStore(ctx, o, nolog)
		if err != nil || p == nil || len(p.cookies) != 1 {
			t.Fatalf("%+v %v", p, err)
		}
		if _, err := os.Stat(filepath.Join(db, "Cookies")); err == nil {
			t.Error("the old database is still in place")
		}
		// The launch fails: everything goes back, nothing is recorded.
		p.restore()
		if data, _ := os.ReadFile(filepath.Join(db, "Cookies")); string(data) != "db Cookies" {
			t.Errorf("not restored: %q", data)
		}
		if st, _ := ReadCookieState(o.ProfileDir); st != nil {
			t.Errorf("state written for a failed launch: %+v", st)
		}
		if es, _ := filepath.Glob(filepath.Join(o.ProfileDir, ".mock-keychain-cookies-*")); len(es) != 0 {
			t.Errorf("left %v", es)
		}
	})

	t.Run("keychain profile untouched", func(t *testing.T) {
		o := keychain
		o.ProfileDir = t.TempDir()
		db := fakeCookieDB(t, o.ProfileDir)
		_ = writeCookieState(o.ProfileDir, CookieState{Store: "keychain", Service: SafeStorageService})
		stubMockReader(t, nil, chromeimport.Stats{}, errors.New("must not be read"))
		if p, err := prepareCookieStore(ctx, o, nolog); err != nil || p != nil {
			t.Fatalf("%v %v", p, err)
		}
		if _, err := os.Stat(filepath.Join(db, "Cookies")); err != nil {
			t.Error(err)
		}
	})
}

func TestTempKeychain(t *testing.T) {
	if _, err := os.Stat("/usr/bin/security"); err != nil {
		t.Skip("no security(1)")
	}
	k, err := NewTempKeychain(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Path, k.Home) {
		t.Errorf("keychain %s outside %s", k.Path, k.Home)
	}
	s, err := k.Services()
	if err != nil || len(s) != 0 {
		t.Errorf("services %v %v", s, err)
	}
	if err := k.Close(); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(k.Home); err == nil {
		t.Error("the temporary home is left")
	}
}
