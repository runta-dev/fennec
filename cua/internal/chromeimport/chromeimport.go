// Package chromeimport reads a user's Google Chrome profiles and their
// cookies so fennec-cua's managed Chromium can be signed in to the same
// sites. It only reads: Chrome's files are copied before they are opened,
// and the cookie key comes from the "Chrome Safe Storage" Keychain item,
// whose access prompt is the user's consent. The key is held in memory
// for one import and never stored, logged or returned; cookie values are
// never logged.
package chromeimport

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultChromeDir is Google Chrome's user data directory on macOS.
func DefaultChromeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

// Profile is one Chrome profile from Local State.
type Profile struct {
	Dir      string `json:"dir"`             // "Default", "Profile 1", ...
	Name     string `json:"name"`            // the profile's display name
	Email    string `json:"email,omitempty"` // the signed-in account, if any
	Account  string `json:"account,omitempty"`
	LastUsed bool   `json:"lastUsed,omitempty"`
	Cookies  bool   `json:"hasCookies"`
}

// ListProfiles reads the profiles of a Chrome user data directory.
func ListProfiles(chromeDir string) ([]Profile, error) {
	data, err := os.ReadFile(filepath.Join(chromeDir, "Local State"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("Google Chrome has no profiles on this Mac")
		}
		return nil, err
	}
	var ls struct {
		Profile struct {
			InfoCache map[string]struct {
				Name     string `json:"name"`
				UserName string `json:"user_name"`
				GaiaName string `json:"gaia_name"`
			} `json:"info_cache"`
			LastUsed      string   `json:"last_used"`
			ProfilesOrder []string `json:"profiles_order"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(data, &ls); err != nil {
		return nil, fmt.Errorf("Chrome's Local State does not read: %w", err)
	}
	order := map[string]int{}
	for i, d := range ls.Profile.ProfilesOrder {
		order[d] = i + 1
	}
	var out []Profile
	for dir, p := range ls.Profile.InfoCache {
		if strings.ContainsAny(dir, `/\`) || dir == "." || dir == ".." {
			continue
		}
		_, err := os.Stat(cookiesPath(filepath.Join(chromeDir, dir)))
		out = append(out, Profile{Dir: dir, Name: p.Name, Email: p.UserName, Account: p.GaiaName, LastUsed: dir == ls.Profile.LastUsed, Cookies: err == nil})
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oj := order[out[i].Dir], order[out[j].Dir]
		if oi != oj && oi != 0 && oj != 0 {
			return oi < oj
		}
		return out[i].Dir < out[j].Dir
	})
	return out, nil
}

// PickProfile chooses a profile by directory or display name; "" picks
// the last used one.
func PickProfile(profiles []Profile, want string) (Profile, error) {
	if len(profiles) == 0 {
		return Profile{}, errors.New("Google Chrome has no profiles on this Mac")
	}
	for _, p := range profiles {
		if want == "" && p.LastUsed || want != "" && (p.Dir == want || strings.EqualFold(p.Name, want) || strings.EqualFold(p.Email, want)) {
			return p, nil
		}
	}
	if want == "" {
		return profiles[0], nil
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = fmt.Sprintf("%q (%s)", p.Name, p.Dir)
	}
	return Profile{}, fmt.Errorf("no Chrome profile %q; the profiles are %s", want, strings.Join(names, ", "))
}

// cookiesPath is the profile's cookie database (Network/Cookies since
// Chrome 96, Cookies before).
func cookiesPath(profileDir string) string {
	p := filepath.Join(profileDir, "Network", "Cookies")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return filepath.Join(profileDir, "Cookies")
}

// KeyProvider answers Chrome's Safe Storage password. The production one
// reads the Keychain (and so asks the user); tests pass a fixed one.
type KeyProvider interface {
	SafeStoragePassword(ctx context.Context) ([]byte, error)
}

// StaticKey is a fixed password (tests).
type StaticKey []byte

// SafeStoragePassword answers the fixed password.
func (k StaticKey) SafeStoragePassword(context.Context) ([]byte, error) {
	return append([]byte(nil), k...), nil
}

// ErrKeyDenied means the user (or a policy) refused the Keychain access.
var ErrKeyDenied = errors.New("access to Chrome's Safe Storage key was not allowed")

// DeriveKey turns the Safe Storage password into the AES-128 key Chrome
// on macOS uses (PBKDF2-HMAC-SHA1, salt "saltysalt", 1003 rounds).
func DeriveKey(password []byte) ([]byte, error) {
	return DeriveKeyFor(password, SaltDefaultDir)
}

// PBKDF2 salts: values tagged "v10" (the default user data directory)
// use SaltDefaultDir; Chromium 160 and later tag values "v11" and use
// SaltCustomDir in a profile started with --user-data-dir (the same
// Keychain password).
const (
	SaltDefaultDir = "saltysalt"
	SaltCustomDir  = "pepper"
)

// DeriveKeyFor derives the AES-128 key with a given salt.
func DeriveKeyFor(password []byte, salt string) ([]byte, error) {
	return pbkdf2.Key(sha1.New, string(password), []byte(salt), 1003, 16)
}

// Decrypt reads an encrypted_value: "v10" or "v11" (the key differs by
// salt; the caller picks it) then AES-128-CBC with an IV of sixteen
// spaces and PKCS#7 padding. Databases at version 24 or later prefix the
// plaintext with SHA-256 of the cookie's host, which is checked and
// dropped.
func Decrypt(key, enc []byte, host string, dbVersion int) ([]byte, error) {
	if len(enc) < 3 || (string(enc[:3]) != "v10" && string(enc[:3]) != "v11") {
		return nil, errors.New("not a v10 or v11 value")
	}
	ct := enc[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := []byte("                ")
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pad := int(pt[len(pt)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(pt) {
		return nil, errors.New("bad padding (wrong key?)")
	}
	for _, b := range pt[len(pt)-pad:] {
		if int(b) != pad {
			return nil, errors.New("bad padding (wrong key?)")
		}
	}
	pt = pt[:len(pt)-pad]
	if dbVersion >= 24 {
		if len(pt) < sha256.Size {
			return nil, errors.New("value is shorter than its host hash")
		}
		sum := sha256.Sum256([]byte(host))
		if string(pt[:sha256.Size]) != string(sum[:]) {
			return nil, errors.New("host hash does not match")
		}
		pt = pt[sha256.Size:]
	}
	return pt, nil
}

// Cookie is a cookie ready for CDP's Storage.setCookies.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"` // a domain cookie (".example.com")
	URL      string  `json:"url,omitempty"`    // a host-only cookie
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	SameSite string  `json:"sameSite,omitempty"` // Strict, Lax, None
	Expires  float64 `json:"expires,omitempty"`  // Unix seconds; 0 is a session cookie
	Priority string  `json:"priority,omitempty"`
	Scheme   string  `json:"sourceScheme,omitempty"`
	Port     int     `json:"sourcePort,omitempty"`
}

// String never shows the value.
func (c Cookie) String() string {
	return fmt.Sprintf("cookie %s for %s%s", c.Name, c.Domain+c.URL, c.Path)
}

// Stats counts what an import read.
type Stats struct {
	Found         int `json:"found"`
	Imported      int `json:"imported"`
	Expired       int `json:"expired"`
	Partitioned   int `json:"partitioned"`
	Undecryptable int `json:"undecryptable"`
}

// chromeEpoch converts Chrome's microseconds since 1601 to Unix seconds.
func chromeEpoch(us int64) float64 {
	return float64(us)/1e6 - 11644473600
}

// ReadCookies reads a profile's cookies, decrypting them with the Safe
// Storage key. The database is copied to a private temporary directory
// first (with its write-ahead log), so a running Chrome is not disturbed.
func ReadCookies(ctx context.Context, profileDir string, keys KeyProvider, now time.Time) ([]Cookie, Stats, error) {
	var st Stats
	src := cookiesPath(profileDir)
	if fi, err := os.Lstat(src); err != nil {
		return nil, st, fmt.Errorf("the profile has no cookie database: %w", err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return nil, st, errors.New("the cookie database is a symbolic link; it is not read")
	}
	tmp, err := os.MkdirTemp("", "fennec-chrome-import-")
	if err != nil {
		return nil, st, err
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, "Cookies")
	if err := copyFile(src, dst); err != nil {
		return nil, st, err
	}
	if _, err := os.Stat(src + "-wal"); err == nil {
		if err := copyFile(src+"-wal", dst+"-wal"); err != nil {
			return nil, st, err
		}
	}
	db, err := openSQLite(dst)
	if err != nil {
		return nil, st, fmt.Errorf("the cookie database does not read: %w", err)
	}
	version := 0
	_ = db.rows("meta", func(r map[string]any) error {
		if r["key"] == "version" {
			switch v := r["value"].(type) {
			case string:
				version, _ = strconv.Atoi(v)
			case int64:
				version = int(v)
			}
		}
		return nil
	})
	type raw struct {
		host, name, path, partition string
		value                       string
		enc                         []byte
		expires                     int64
		secure, httpOnly, persist   bool
		sameSite, priority, scheme  int64
		port                        int64
	}
	var rows []raw
	err = db.rows("cookies", func(r map[string]any) error {
		str := func(k string) string { s, _ := r[k].(string); return s }
		num := func(k string) int64 { n, _ := r[k].(int64); return n }
		enc, _ := r["encrypted_value"].([]byte)
		persist := true
		if _, ok := r["is_persistent"]; ok {
			persist = num("is_persistent") != 0
		} else if _, ok := r["has_expires"]; ok {
			persist = num("has_expires") != 0
		}
		port := int64(-1)
		if _, ok := r["source_port"]; ok {
			port = num("source_port")
		}
		rows = append(rows, raw{host: str("host_key"), name: str("name"), path: str("path"), partition: str("top_frame_site_key"),
			value: str("value"), enc: enc, expires: num("expires_utc"), secure: num("is_secure") != 0, httpOnly: num("is_httponly") != 0,
			persist: persist, sameSite: num("samesite"), priority: num("priority"), scheme: num("source_scheme"), port: port})
		return nil
	})
	if err != nil {
		return nil, st, fmt.Errorf("the cookie database does not read: %w", err)
	}
	var key, key11 []byte
	defer func() { clear(key); clear(key11) }()
	needKey := false
	for _, r := range rows {
		if len(r.enc) > 0 {
			needKey = true
			break
		}
	}
	if needKey {
		pw, err := keys.SafeStoragePassword(ctx)
		if err != nil {
			return nil, st, err
		}
		key, err = DeriveKey(pw)
		if err == nil {
			key11, err = DeriveKeyFor(pw, SaltCustomDir)
		}
		clear(pw)
		if err != nil {
			return nil, st, err
		}
	}
	var out []Cookie
	for _, r := range rows {
		st.Found++
		if r.partition != "" {
			st.Partitioned++ // partitioned (CHIPS) cookies are left out
			continue
		}
		c := Cookie{Name: r.name, Path: r.path, Secure: r.secure, HTTPOnly: r.httpOnly}
		if r.persist && r.expires != 0 {
			c.Expires = chromeEpoch(r.expires)
			if c.Expires <= float64(now.Unix()) {
				st.Expired++
				continue
			}
		}
		if len(r.enc) > 0 {
			k := key
			if len(r.enc) >= 3 && string(r.enc[:3]) == "v11" {
				k = key11
			}
			v, err := Decrypt(k, r.enc, r.host, version)
			if err != nil {
				st.Undecryptable++
				continue
			}
			c.Value = string(v)
		} else {
			c.Value = r.value
		}
		if c.Path == "" {
			c.Path = "/"
		}
		if strings.HasPrefix(r.host, ".") {
			c.Domain = r.host
		} else {
			scheme := "http"
			if c.Secure || r.scheme == 2 {
				scheme = "https"
			}
			c.URL = scheme + "://" + r.host + c.Path
		}
		switch r.sameSite {
		case 0:
			c.SameSite = "None"
		case 1:
			c.SameSite = "Lax"
		case 2:
			c.SameSite = "Strict"
		}
		if c.SameSite == "None" && !c.Secure {
			c.SameSite = ""
		}
		switch r.priority {
		case 0:
			c.Priority = "Low"
		case 2:
			c.Priority = "High"
		default:
			c.Priority = "Medium"
		}
		switch r.scheme {
		case 1:
			c.Scheme = "NonSecure"
		case 2:
			c.Scheme = "Secure"
		}
		if r.port > 0 && r.port <= 65535 {
			c.Port = int(r.port)
		}
		out = append(out, c)
	}
	return out, st, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
