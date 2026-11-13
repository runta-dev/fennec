//go:build chromiumlive

package browser

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"fennec/cua/internal/chromeimport"
	"fennec/cua/internal/chromium"
)

// Live tests against the pinned Chromium, headless, on a temporary
// profile, with pages served on 127.0.0.1 only. The Chrome profile they
// import from is synthetic, encrypted with a test key: the user's Chrome
// and Keychain are never read. Run with
// FENNEC_CUA_CHROMIUM=/path/to/Chromium.app go test -tags chromiumlive ./internal/browser

func liveBrowser(t *testing.T) (*chromium.Browser, string) {
	t.Helper()
	bin := chromium.Locate("")
	if bin == "" {
		t.Skip("set FENNEC_CUA_CHROMIUM to the pinned Chromium.app")
	}
	dir, err := os.MkdirTemp("", "fccr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := chromium.New(chromium.Options{Bin: bin, ProfileDir: dir, Headless: true, MockKeychain: true, Log: t.Logf})
	t.Cleanup(b.Close)
	return b, dir
}

// syntheticChrome writes a Chrome user data directory with one profile
// whose cookies are encrypted with password (meta version 24).
func syntheticChrome(t *testing.T, password string, host string) string {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(`{"profile":{"last_used":"Default","info_cache":{"Default":{"name":"Test","user_name":"test@example.test"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	net := filepath.Join(dir, "Default", "Network")
	if err := os.MkdirAll(net, 0o700); err != nil {
		t.Fatal(err)
	}
	key, _ := chromeimport.DeriveKey([]byte(password))
	enc := func(host, v string) string {
		sum := sha256.Sum256([]byte(host))
		plain := append(sum[:], v...)
		pad := aes.BlockSize - len(plain)%aes.BlockSize
		for i := 0; i < pad; i++ {
			plain = append(plain, byte(pad))
		}
		block, _ := aes.NewCipher(key)
		cipher.NewCBCEncrypter(block, []byte("                ")).CryptBlocks(plain, plain)
		return hex.EncodeToString(append([]byte("v10"), plain...))
	}
	exp := (time.Now().Add(48*time.Hour).Unix() + 11644473600) * 1e6
	sql := `CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
INSERT INTO meta VALUES('version','24');
CREATE TABLE cookies(creation_utc INTEGER NOT NULL,host_key TEXT NOT NULL,top_frame_site_key TEXT NOT NULL,name TEXT NOT NULL,value TEXT NOT NULL,encrypted_value BLOB NOT NULL,path TEXT NOT NULL,expires_utc INTEGER NOT NULL,is_secure INTEGER NOT NULL,is_httponly INTEGER NOT NULL,last_access_utc INTEGER NOT NULL,has_expires INTEGER NOT NULL,is_persistent INTEGER NOT NULL,priority INTEGER NOT NULL,samesite INTEGER NOT NULL,source_scheme INTEGER NOT NULL,source_port INTEGER NOT NULL,last_update_utc INTEGER NOT NULL,source_type INTEGER NOT NULL,has_cross_site_ancestor INTEGER NOT NULL);
` + fmt.Sprintf("INSERT INTO cookies VALUES(1,'%s','','session_id','',X'%s','/',%d,0,1,0,1,1,1,1,1,-1,0,0,0);\n", host, enc(host, "imported-value-42"), exp) +
		fmt.Sprintf("INSERT INTO cookies VALUES(2,'%s','','theme','',X'%s','/',0,0,0,0,0,0,1,1,1,-1,0,0,0);\n", host, enc(host, "dark"))
	cmd := exec.Command(bin, filepath.Join(net, "Cookies"))
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
	return dir
}

func TestLiveImportFromChrome(t *testing.T) {
	b, profile := liveBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>Cookies</title><p id=c>%s</p>", r.Header.Get("Cookie"))
	}))
	defer srv.Close()
	chrome := syntheticChrome(t, "live-test-key", "127.0.0.1")
	status := filepath.Join(t.TempDir(), "import.json")
	m := NewManager(Options{Browser: b, Log: t.Logf, Import: &ImportConfig{ChromeDir: chrome, Keys: chromeimport.StaticKey("live-test-key"), StatusFile: status}})
	defer m.CloseAll(context.Background())
	ps, err := m.ChromeProfiles()
	if err != nil || len(ps) != 1 || ps[0].Email != "test@example.test" {
		t.Fatalf("profiles %+v %v", ps, err)
	}
	res, err := m.ImportFromChrome(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cookies.Found != 2 || res.Cookies.Imported != 2 || res.Failed != 0 {
		t.Errorf("result %+v", res)
	}
	data, _ := os.ReadFile(status)
	if strings.Contains(string(data), "imported-value-42") || !strings.Contains(string(data), `"imported":2`) {
		t.Errorf("status file %s", data)
	}
	out := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": srv.URL + "/"})
	text := out["text"].(string)
	if !strings.Contains(text, "session_id=imported-value-42") || !strings.Contains(text, "theme=dark") {
		t.Errorf("the page did not get the imported cookies:\n%s", text)
	}
	// Replace clears the managed cookies first.
	if _, err := m.ImportFromChrome(context.Background(), ImportOptions{Replace: true, Profile: "Test"}); err != nil {
		t.Fatal(err)
	}
	// The managed profile stays in its own directory, owner-only.
	if st, err := os.Stat(profile); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("profile %v %v", st, err)
	}
}

// A real crash: the browser is killed; the next tab starts it again.
func TestLiveCrashAndTurn(t *testing.T) {
	b, profile := liveBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<!doctype html><title>Page</title><button>Go</button>")
	}))
	defer srv.Close()
	m := NewManager(Options{Browser: b, Log: t.Logf})
	defer m.CloseAll(context.Background())
	res := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": srv.URL + "/a"})
	keep := res["binding"]
	mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": srv.URL + "/b"})
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": keep, "method": "markDeliverable"})
	m.TurnEnded(context.Background(), "s")
	res = mustCall(t, m, "s", map[string]any{"op": "listTabs"})
	if tabs := res["tabs"].([]any); len(tabs) != 1 {
		t.Errorf("after the turn %v", tabs)
	}
	lock, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	fmt.Sscanf(lock[strings.LastIndexByte(lock, '-')+1:], "%d", &pid)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": keep, "method": "getAXState"}); perr == nil {
		t.Error("a tab survived the crash")
	}
	res = mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": srv.URL + "/c"})
	if !strings.Contains(res["text"].(string), "button Go") {
		t.Errorf("after restart %v", res["text"])
	}
}
