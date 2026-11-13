package chromeimport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic Chrome profiles: a Local State and a Cookies database built by
// the test with the system sqlite3 tool and a test key. The user's real
// Chrome profile and Keychain are never read.

const testPassword = "test-safe-storage-password"

func encrypt(t *testing.T, plain []byte) []byte {
	t.Helper()
	key, err := DeriveKey([]byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, []byte("                ")).CryptBlocks(buf, buf)
	return append([]byte("v10"), buf...)
}

// chromeTime is Unix seconds as Chrome's microseconds since 1601.
func chromeTime(unix int64) int64 { return (unix + 11644473600) * 1e6 }

type fakeCookie struct {
	host, name, value, path, partition string
	expires                            int64 // unix; 0 session
	secure, httpOnly                   bool
	sameSite                           int
	plain                              bool // stored in value, not encrypted
	tamper                             bool // encrypted for another host
}

func sqlite3(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	return p
}

// makeProfile writes a profile's Network/Cookies at a meta version; wal
// leaves the rows in a write-ahead log beside a copy of the database.
func makeProfile(t *testing.T, dir string, version int, cookies []fakeCookie, wal bool) {
	t.Helper()
	bin := sqlite3(t)
	net := filepath.Join(dir, "Network")
	if err := os.MkdirAll(net, 0o700); err != nil {
		t.Fatal(err)
	}
	var sql strings.Builder
	if wal {
		sql.WriteString("PRAGMA journal_mode=WAL;\nPRAGMA wal_autocheckpoint=0;\n")
	}
	sql.WriteString(`CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
CREATE TABLE cookies(creation_utc INTEGER NOT NULL,host_key TEXT NOT NULL,top_frame_site_key TEXT NOT NULL,name TEXT NOT NULL,value TEXT NOT NULL,encrypted_value BLOB NOT NULL,path TEXT NOT NULL,expires_utc INTEGER NOT NULL,is_secure INTEGER NOT NULL,is_httponly INTEGER NOT NULL,last_access_utc INTEGER NOT NULL,has_expires INTEGER NOT NULL,is_persistent INTEGER NOT NULL,priority INTEGER NOT NULL,samesite INTEGER NOT NULL,source_scheme INTEGER NOT NULL,source_port INTEGER NOT NULL,last_update_utc INTEGER NOT NULL,source_type INTEGER NOT NULL,has_cross_site_ancestor INTEGER NOT NULL);
CREATE UNIQUE INDEX cookies_unique_index ON cookies(host_key, top_frame_site_key, has_cross_site_ancestor, name, path, source_scheme, source_port);
`)
	fmt.Fprintf(&sql, "INSERT INTO meta VALUES('version','%d');\n", version)
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	for i, c := range cookies {
		value, enc := "", ""
		if c.plain {
			value = c.value
		} else {
			host := c.host
			if c.tamper {
				host = "other.test"
			}
			plain := []byte(c.value)
			if version >= 24 {
				sum := sha256.Sum256([]byte(host))
				plain = append(sum[:], plain...)
			}
			enc = hex.EncodeToString(encrypt(t, plain))
		}
		exp, has := int64(0), c.expires != 0
		if has {
			exp = chromeTime(c.expires)
		}
		scheme := 1
		if c.secure {
			scheme = 2
		}
		fmt.Fprintf(&sql, "INSERT INTO cookies VALUES(%d,'%s','%s','%s','%s',X'%s','%s',%d,%d,%d,0,%d,%d,1,%d,%d,443,0,0,0);\n",
			chromeTime(1700000000)+int64(i), c.host, c.partition, c.name, value, enc, c.path, exp, b(c.secure), b(c.httpOnly), b(has), b(has), c.sameSite, scheme)
	}
	db := filepath.Join(net, "Cookies")
	target := db
	if wal {
		// Keep the log: copy the files while the writer still has them open.
		target = filepath.Join(t.TempDir(), "Cookies")
		fmt.Fprintf(&sql, ".shell cp %q %q; cp %q %q\n", target, db, target+"-wal", db+"-wal")
	}
	cmd := exec.Command(bin, target)
	cmd.Stdin = strings.NewReader(sql.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	if wal {
		if st, err := os.Stat(db + "-wal"); err != nil || st.Size() == 0 {
			t.Fatalf("no write-ahead log was kept: %v", err)
		}
	}
}

func sampleCookies() []fakeCookie {
	future := time.Now().Add(240 * time.Hour).Unix()
	long := strings.Repeat("L", 5000) // overflows a 4 KB page
	return []fakeCookie{
		{host: ".example.test", name: "sid", value: "s3cret-session", path: "/", expires: future, secure: true, httpOnly: true, sameSite: 1},
		{host: "app.example.test", name: "__Host-x", value: "host-only", path: "/", expires: future, secure: true, sameSite: 2},
		{host: ".example.test", name: "pref", value: "dark", path: "/", plain: true},
		{host: ".example.test", name: "old", value: "gone", path: "/", expires: time.Now().Add(-time.Hour).Unix()},
		{host: ".embed.test", name: "chip", value: "p", path: "/", expires: future, secure: true, partition: "https://top.test"},
		{host: ".example.test", name: "bad", value: "x", path: "/", expires: future, tamper: true},
		{host: ".big.test", name: "big", value: long, path: "/a", expires: future, secure: true, sameSite: 0},
	}
}

func checkCookies(t *testing.T, got []Cookie, st Stats, version int) {
	t.Helper()
	by := map[string]Cookie{}
	for _, c := range got {
		by[c.Name] = c
	}
	if c := by["sid"]; c.Value != "s3cret-session" || c.Domain != ".example.test" || c.URL != "" || !c.Secure || !c.HTTPOnly || c.SameSite != "Lax" || c.Expires < float64(time.Now().Unix()) {
		t.Errorf("sid %+v", c)
	}
	if c := by["__Host-x"]; c.Value != "host-only" || c.Domain != "" || c.URL != "https://app.example.test/" || c.SameSite != "Strict" || c.Scheme != "Secure" || c.Port != 443 {
		t.Errorf("host-only %+v", c)
	}
	if c := by["pref"]; c.Value != "dark" || c.Expires != 0 {
		t.Errorf("session %+v", c)
	}
	if c := by["big"]; len(c.Value) != 5000 || c.SameSite != "None" || c.Path != "/a" {
		t.Errorf("big %d %q", len(c.Value), c.SameSite)
	}
	wantBad := 0
	if version >= 24 {
		wantBad = 1 // the host hash does not match
	}
	if st.Found != 7 || st.Expired != 1 || st.Partitioned != 1 || st.Undecryptable != wantBad || len(got) != 7-2-wantBad {
		t.Errorf("stats %+v, %d cookies", st, len(got))
	}
}

func TestReadCookiesBothVersions(t *testing.T) {
	for _, version := range []int{23, 24} {
		for _, wal := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d-wal%v", version, wal), func(t *testing.T) {
				dir := t.TempDir()
				makeProfile(t, dir, version, sampleCookies(), wal)
				got, st, err := ReadCookies(context.Background(), dir, StaticKey(testPassword), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				checkCookies(t, got, st, version)
			})
		}
	}
}

// A wrong key reads nothing, and no value appears in any error or string.
func TestWrongKeyAndSecrets(t *testing.T) {
	dir := t.TempDir()
	makeProfile(t, dir, 24, sampleCookies(), false)
	got, st, err := ReadCookies(context.Background(), dir, StaticKey("wrong"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.Name != "pref" {
			t.Errorf("decrypted with the wrong key: %s", c)
		}
	}
	if st.Undecryptable != 4 {
		t.Errorf("stats %+v", st)
	}
	got, _, _ = ReadCookies(context.Background(), dir, StaticKey(testPassword), time.Now())
	for _, c := range got {
		if strings.Contains(c.String(), c.Value) && c.Value != "" {
			t.Errorf("String shows the value: %s", c)
		}
		if strings.Contains(fmt.Sprint(c), "s3cret") {
			t.Errorf("formatting shows the value: %v", c)
		}
	}
	denied := keyFunc(func(context.Context) ([]byte, error) { return nil, ErrKeyDenied })
	if _, _, err := ReadCookies(context.Background(), dir, denied, time.Now()); !errors.Is(err, ErrKeyDenied) {
		t.Errorf("denied: %v", err)
	}
}

type keyFunc func(context.Context) ([]byte, error)

func (f keyFunc) SafeStoragePassword(ctx context.Context) ([]byte, error) { return f(ctx) }

func TestDecrypt(t *testing.T) {
	key, _ := DeriveKey([]byte(testPassword))
	if hex.EncodeToString(key) == "" || len(key) != 16 {
		t.Fatal("key")
	}
	sum := sha256.Sum256([]byte(".a.test"))
	enc := encrypt(t, append(sum[:], "v"...))
	if v, err := Decrypt(key, enc, ".a.test", 24); err != nil || string(v) != "v" {
		t.Errorf("%q %v", v, err)
	}
	if _, err := Decrypt(key, enc, ".b.test", 24); err == nil {
		t.Error("another host's hash was accepted")
	}
	if _, err := Decrypt(key, []byte("v11xxxx"), "", 0); err == nil {
		t.Error("a malformed v11 value accepted")
	}
	if _, err := Decrypt(key, append([]byte("v10"), make([]byte, 15)...), "", 0); err == nil {
		t.Error("short block accepted")
	}
}

func TestListProfiles(t *testing.T) {
	dir := t.TempDir()
	state := `{"profile":{"last_used":"Profile 1","profiles_order":["Default","Profile 1"],"info_cache":{
	  "Default":{"name":"Personal","user_name":"me@example.test","gaia_name":"Me"},
	  "Profile 1":{"name":"Work","user_name":""},
	  "../evil":{"name":"x"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	makeProfile(t, filepath.Join(dir, "Profile 1"), 24, nil, false)
	ps, err := ListProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Dir != "Default" || ps[0].Email != "me@example.test" || ps[0].Cookies || !ps[1].LastUsed || !ps[1].Cookies {
		t.Errorf("%+v", ps)
	}
	if p, err := PickProfile(ps, ""); err != nil || p.Dir != "Profile 1" {
		t.Errorf("last used %v %v", p, err)
	}
	if p, err := PickProfile(ps, "personal"); err != nil || p.Dir != "Default" {
		t.Errorf("by name %v %v", p, err)
	}
	if _, err := PickProfile(ps, "nope"); err == nil || !strings.Contains(err.Error(), `"Work" (Profile 1)`) {
		t.Errorf("unknown %v", err)
	}
	if _, err := ListProfiles(t.TempDir()); err == nil {
		t.Error("no Local State")
	}
}

// TestDecryptV11 covers values Chromium 160 writes in a profile started
// with --user-data-dir: tag "v11", the same password, salt "pepper".
func TestDecryptV11(t *testing.T) {
	key11, err := DeriveKeyFor([]byte(testPassword), SaltCustomDir)
	if err != nil {
		t.Fatal(err)
	}
	key10, _ := DeriveKey([]byte(testPassword))
	if bytes.Equal(key10, key11) {
		t.Fatal("the salts give the same key")
	}
	plain := []byte("value")
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key11)
	cipher.NewCBCEncrypter(block, []byte("                ")).CryptBlocks(buf, buf)
	enc := append([]byte("v11"), buf...)
	if v, err := Decrypt(key11, enc, "", 0); err != nil || string(v) != "value" {
		t.Errorf("%q %v", v, err)
	}
	if _, err := Decrypt(key10, enc, "", 0); err == nil {
		t.Error("the v10 key opened a v11 value")
	}
}
