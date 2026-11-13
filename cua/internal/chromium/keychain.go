package chromium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fennec/cua/internal/cdp"
	"fennec/cua/internal/chromeimport"
)

// Cookie encryption. Chromium on macOS encrypts cookie values with a key
// derived from a generic-password Keychain item it reads with
// SecItemCopyMatching (service "Chromium Safe Storage", account
// "Chromium" in this build) and creates, with 16 random bytes, when it is
// missing. The item is created by the signed Chromium, so its access list
// trusts that app alone and macOS does not ask. --use-mock-keychain
// instead uses the fixed public password "mock_password", which protects
// nothing.
//
// cua-package renames the item to "Fennec Safe Storage" in the bundled
// build (the service-name constant in the framework, padded with NULs;
// verified on every staging with a temporary keychain) and records that
// in the app (SafeStorageMarker). The daemon uses the real Keychain only
// for a build carrying that marker; any other build (a development
// override) keeps the mock keychain, so the user's own "Chromium Safe
// Storage" item is never read, created or changed.
const (
	SafeStorageService = "Fennec Safe Storage"
	SafeStorageAccount = "Chromium"
	// UpstreamSafeStorageService is the stock build's item.
	UpstreamSafeStorageService = "Chromium Safe Storage"
	// SafeStorageMarker is the marker file in Chromium.app/Contents/Resources.
	SafeStorageMarker = "fennec-safe-storage.json"
	// CookieStateFile records, in the profile, how its cookies are
	// encrypted.
	CookieStateFile = "fennec-cookie-store.json"
	// mockPassword is --use-mock-keychain's fixed password.
	mockPassword = "mock_password"
)

// SafeStorage is the Keychain item a bundled build keeps its key in.
type SafeStorage struct {
	Service string `json:"service"`
	Account string `json:"account"`
}

// AppDir answers the Chromium.app of an executable inside it.
func AppDir(bin string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(bin)))
}

// SafeStorageOf reads the marker of the app holding bin; false means a
// build whose item is not renamed (it must run with the mock keychain).
func SafeStorageOf(bin string) (SafeStorage, bool) {
	data, err := os.ReadFile(filepath.Join(AppDir(bin), "Contents", "Resources", SafeStorageMarker))
	if err != nil {
		return SafeStorage{}, false
	}
	var s SafeStorage
	if json.Unmarshal(data, &s) != nil || s.Service != SafeStorageService || s.Account != SafeStorageAccount {
		return SafeStorage{}, false
	}
	return s, true
}

// CookieState is what the profile's cookies are encrypted with.
type CookieState struct {
	Store     string     `json:"store"` // "keychain" or "mock"
	Service   string     `json:"service,omitempty"`
	Migration *Migration `json:"migration,omitempty"`
	At        time.Time  `json:"at"`
}

// Migration reports moving a mock-keychain profile's cookies to the
// Keychain key. It never carries values.
type Migration struct {
	Found         int    `json:"found"`
	Carried       int    `json:"carried"`
	Expired       int    `json:"expired,omitempty"`
	Partitioned   int    `json:"partitioned,omitempty"`
	Undecryptable int    `json:"undecryptable,omitempty"`
	Failed        int    `json:"failed,omitempty"`
	Note          string `json:"note,omitempty"`
}

// ReadCookieState answers the profile's state; nil when none is recorded.
func ReadCookieState(profile string) (*CookieState, error) {
	data, err := os.ReadFile(filepath.Join(profile, CookieStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s CookieState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", CookieStateFile, err)
	}
	return &s, nil
}

func writeCookieState(profile string, s CookieState) error {
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := filepath.Join(profile, CookieStateFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(profile, CookieStateFile))
}

// ErrKeychainProfile is a profile whose cookies use the Keychain key,
// started with the mock keychain: they would be unreadable and dropped.
var ErrKeychainProfile = errors.New("the browser profile's cookies are encrypted with the Fennec Safe Storage Keychain item, but this Chromium build cannot use it; use the bundled Chromium, or another profile (" + EnvProfile + ")")

// cookieFiles are the cookie database and its journals in a profile.
func cookieFiles(profile string) (dir string, names []string) {
	dir = filepath.Join(profile, "Default", "Network")
	for _, n := range []string{"Cookies", "Cookies-journal", "Cookies-wal", "Cookies-shm"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			names = append(names, n)
		}
	}
	return dir, names
}

// pendingMigration holds a mock-keychain profile's cookies between the
// moment the old database is set aside and the moment the browser, now
// on the Keychain key, has them again.
type pendingMigration struct {
	cookies []chromeimport.Cookie
	stats   chromeimport.Stats
	readErr error
	dbDir   string
	aside   string // where the old files wait
	names   []string
}

// readMockCookies decrypts a profile's cookies with the mock password
// (a variable for tests).
var readMockCookies = func(ctx context.Context, profile string) ([]chromeimport.Cookie, chromeimport.Stats, error) {
	return chromeimport.ReadCookies(ctx, filepath.Join(profile, "Default"), chromeimport.StaticKey(mockPassword), time.Now())
}

// prepareCookieStore runs before a launch. With the mock keychain it
// refuses a Keychain profile; with the Keychain it reads the cookies of a
// mock-keychain profile and sets the old database aside, so the browser
// starts on an empty store and finishMigration puts them back encrypted
// with the Keychain key.
func prepareCookieStore(ctx context.Context, o Options, logf func(string, ...any)) (*pendingMigration, error) {
	st, err := ReadCookieState(o.ProfileDir)
	if err != nil {
		return nil, err
	}
	if o.MockKeychain {
		if st != nil && st.Store == "keychain" {
			return nil, ErrKeychainProfile
		}
		if st == nil {
			return nil, writeCookieState(o.ProfileDir, CookieState{Store: "mock", At: time.Now().UTC()})
		}
		return nil, nil
	}
	if st != nil && st.Store == "keychain" {
		return nil, nil
	}
	dbDir, names := cookieFiles(o.ProfileDir)
	if len(names) == 0 {
		return nil, writeCookieState(o.ProfileDir, CookieState{Store: "keychain", Service: o.SafeStorageService, At: time.Now().UTC()})
	}
	p := &pendingMigration{dbDir: dbDir, names: names}
	p.cookies, p.stats, p.readErr = readMockCookies(ctx, o.ProfileDir)
	aside, err := os.MkdirTemp(o.ProfileDir, ".mock-keychain-cookies-")
	if err != nil {
		return nil, err
	}
	p.aside = aside
	for _, n := range names {
		if err := os.Rename(filepath.Join(dbDir, n), filepath.Join(aside, n)); err != nil {
			p.restore()
			return nil, fmt.Errorf("setting the old cookie database aside: %w", err)
		}
	}
	logf("chromium: moving the profile's cookies from the mock keychain to the %q Keychain item", o.SafeStorageService)
	return p, nil
}

// restore puts the old database back (the launch failed).
func (p *pendingMigration) restore() {
	for _, n := range p.names {
		_ = os.Rename(filepath.Join(p.aside, n), filepath.Join(p.dbDir, n))
	}
	_ = os.Remove(p.aside)
	clear(p.cookies)
}

// finish sets the cookies in the running browser, records the result and
// drops the old database. Whatever could not be carried is said, in the
// log and in the profile's state (shown by `fennec-cua browser status`).
func (p *pendingMigration) finish(ctx context.Context, c *cdp.Conn, o Options, logf func(string, ...any)) error {
	m := &Migration{Found: p.stats.Found, Expired: p.stats.Expired, Partitioned: p.stats.Partitioned, Undecryptable: p.stats.Undecryptable}
	if p.readErr != nil {
		m.Note = "the earlier cookies could not be read (" + p.readErr.Error() + "); sign in again or import from Chrome again"
	} else {
		call := func(cs []chromeimport.Cookie) error {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := c.Call(cctx, "", "Storage.setCookies", map[string]any{"cookies": cs})
			return err
		}
		const batch = 200
		for i := 0; i < len(p.cookies); i += batch {
			part := p.cookies[i:min(i+batch, len(p.cookies))]
			if call(part) == nil {
				m.Carried += len(part)
				continue
			}
			for _, ck := range part {
				if call([]chromeimport.Cookie{ck}) == nil {
					m.Carried++
				} else {
					m.Failed++
				}
			}
		}
		if lost := m.Found - m.Carried - m.Expired; lost > 0 {
			m.Note = fmt.Sprintf("%d cookies could not be carried over (partitioned, unreadable or refused); sign in again or import from Chrome again", lost)
		}
	}
	clear(p.cookies)
	p.cookies = nil
	if err := writeCookieState(o.ProfileDir, CookieState{Store: "keychain", Service: o.SafeStorageService, Migration: m, At: time.Now().UTC()}); err != nil {
		return err
	}
	_ = os.RemoveAll(p.aside)
	logf("chromium: cookies now use the Keychain: carried %d of %d (expired %d, partitioned %d, unreadable %d, refused %d)%s",
		m.Carried, m.Found, m.Expired, m.Partitioned, m.Undecryptable, m.Failed, map[bool]string{true: "; " + m.Note, false: ""}[m.Note != ""])
	return nil
}
