package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"fennec/cua/internal/chromeimport"
	"fennec/cua/internal/protocol"
)

// ImportConfig configures "Import from Chrome".
type ImportConfig struct {
	// ChromeDir is Google Chrome's user data directory.
	ChromeDir string
	// Keys answers Chrome's Safe Storage password (the Keychain).
	Keys chromeimport.KeyProvider
	// StatusFile records the last import's summary (never values).
	StatusFile string
	Now        func() time.Time
}

// ImportOptions choose what to import.
type ImportOptions struct {
	// Profile is a Chrome profile directory or name; "" is the last used.
	Profile string `json:"profile,omitempty"`
	// Replace clears the managed browser's cookies first.
	Replace bool `json:"replace,omitempty"`
}

// ImportResult summarises an import. It never carries cookie values.
type ImportResult struct {
	Profile  chromeimport.Profile `json:"profile"`
	Cookies  chromeimport.Stats   `json:"cookies"`
	Failed   int                  `json:"failed,omitempty"`
	Replaced bool                 `json:"replaced,omitempty"`
	At       time.Time            `json:"at"`
}

// ChromeProfiles lists the user's Chrome profiles.
func (m *Manager) ChromeProfiles() ([]chromeimport.Profile, error) {
	if m.opts.Import == nil {
		return nil, errors.New("import from Chrome is not available")
	}
	return chromeimport.ListProfiles(m.opts.Import.ChromeDir)
}

// LastImport answers the last import's summary, if any.
func (m *Manager) LastImport() (*ImportResult, error) {
	if m.opts.Import == nil || m.opts.Import.StatusFile == "" {
		return nil, nil
	}
	data, err := os.ReadFile(m.opts.Import.StatusFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r ImportResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ImportFromChrome copies a Chrome profile's cookies into the managed
// Chromium (Storage.setCookies), so agent tabs are signed in where the
// user is. Reading the cookie key asks the user through the Keychain;
// running it again refreshes the cookies (same name, domain and path
// replace the earlier copy).
func (m *Manager) ImportFromChrome(ctx context.Context, o ImportOptions) (ImportResult, error) {
	var res ImportResult
	cfg := m.opts.Import
	if cfg == nil {
		return res, errors.New("import from Chrome is not available")
	}
	if m.opts.Browser == nil || !m.opts.Browser.Available() {
		return res, errors.New("the bundled Chromium is missing")
	}
	m.importMu.Lock()
	defer m.importMu.Unlock()
	profiles, err := chromeimport.ListProfiles(cfg.ChromeDir)
	if err != nil {
		return res, err
	}
	p, err := chromeimport.PickProfile(profiles, o.Profile)
	if err != nil {
		return res, err
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	cookies, st, err := chromeimport.ReadCookies(ctx, filepath.Join(cfg.ChromeDir, p.Dir), cfg.Keys, now())
	if err != nil {
		return res, err
	}
	res.Profile, res.Cookies = p, st
	conn, _, err := m.opts.Browser.Conn(ctx)
	if err != nil {
		return res, err
	}
	call := func(method string, params any) error {
		cctx, cancel := context.WithTimeout(ctx, m.timeout())
		defer cancel()
		_, err := conn.Call(cctx, "", method, params)
		return err
	}
	if o.Replace {
		if err := call("Storage.clearCookies", nil); err != nil {
			return res, err
		}
		res.Replaced = true
	}
	const batch = 200
	for i := 0; i < len(cookies); i += batch {
		part := cookies[i:min(i+batch, len(cookies))]
		if err := call("Storage.setCookies", map[string]any{"cookies": part}); err == nil {
			res.Cookies.Imported += len(part)
			continue
		} else if ctx.Err() != nil {
			return res, err
		}
		// One bad cookie fails its batch: set them one by one.
		for _, c := range part {
			if err := call("Storage.setCookies", map[string]any{"cookies": []chromeimport.Cookie{c}}); err != nil {
				res.Failed++
			} else {
				res.Cookies.Imported++
			}
		}
	}
	clear(cookies)
	res.At = now().UTC()
	if cfg.StatusFile != "" {
		if data, err := json.Marshal(res); err == nil {
			_ = os.MkdirAll(filepath.Dir(cfg.StatusFile), 0o700)
			_ = os.WriteFile(cfg.StatusFile, data, 0o600)
		}
	}
	m.logf("browser: imported %d of %d cookies from Chrome profile %q", res.Cookies.Imported, res.Cookies.Found, p.Dir)
	return res, nil
}

// replProfiles answers fennec.browser.chromeProfiles().
func (m *Manager) replProfiles() (any, *protocol.Error) {
	ps, err := m.ChromeProfiles()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: err.Error()}
	}
	if ps == nil {
		ps = []chromeimport.Profile{}
	}
	return ps, nil
}

// replImport answers fennec.browser.importFromChrome(): the user confirms
// through the confirm gate (when on) and always through the Keychain.
func (m *Manager) replImport(ctx context.Context, req request) (any, *protocol.Error) {
	if !m.allowed() {
		return nil, &protocol.Error{Code: protocol.ErrNotAllowed, Message: AppName + " is outside the app allowlist; nothing was imported"}
	}
	if m.opts.Gate != nil {
		if perr := m.opts.Gate.Ask(ctx, []protocol.Step{{Perform: &protocol.Target{Select: "Google Chrome sign-ins"}, Action: "import into " + BrowserName}}, BrowserName); perr != nil {
			return nil, perr
		}
	}
	res, err := m.ImportFromChrome(ctx, ImportOptions{Profile: req.Profile, Replace: req.Replace})
	if err != nil {
		code := protocol.ErrInternal
		if errors.Is(err, chromeimport.ErrKeyDenied) {
			code = protocol.ErrDeclined
		} else if errors.Is(err, context.Canceled) {
			code = protocol.ErrStopped
		}
		return nil, &protocol.Error{Code: code, Message: "import from Chrome: " + err.Error()}
	}
	return res, nil
}
