package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/chromeimport"
	"fennec/cua/internal/protocol"
)

// fakeChrome writes a synthetic Chrome user data directory whose cookies
// are stored unencrypted (the value column), so no key is needed; one
// cookie is named "reject", which the fake browser refuses.
func fakeChrome(t *testing.T, n int) string {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Local State"), []byte(`{"profile":{"last_used":"Default","info_cache":{"Default":{"name":"Me"}}}}`), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "Default", "Network"), 0o700)
	var sql strings.Builder
	sql.WriteString("CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);\nINSERT INTO meta VALUES('version','24');\n")
	sql.WriteString("CREATE TABLE cookies(host_key TEXT, top_frame_site_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT, expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, has_expires INTEGER, is_persistent INTEGER, samesite INTEGER, source_scheme INTEGER, source_port INTEGER);\nBEGIN;\n")
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("c%d", i)
		if i == 7 {
			name = "reject"
		}
		fmt.Fprintf(&sql, "INSERT INTO cookies VALUES('.site%d.test','','%s','secret-%d',X'','/',0,1,0,0,0,1,2,443);\n", i%3, name, i)
	}
	sql.WriteString("COMMIT;\n")
	cmd := exec.Command(bin, filepath.Join(dir, "Default", "Network", "Cookies"))
	cmd.Stdin = strings.NewReader(sql.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
	return dir
}

func TestImportFromChrome(t *testing.T) {
	f := newFake()
	chrome := fakeChrome(t, 450)
	status := filepath.Join(t.TempDir(), "s", "import.json")
	keys := 0
	g := &gateRec{}
	var logged []string
	m := NewManager(Options{Browser: f, Gate: g, Log: func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) },
		Import: &ImportConfig{ChromeDir: chrome, StatusFile: status, Keys: keyCount(&keys), Now: func() time.Time { return time.Unix(1800000000, 0) }}})
	defer m.CloseAll(context.Background())
	if last, err := m.LastImport(); err != nil || last != nil {
		t.Errorf("before: %v %v", last, err)
	}
	// From the REPL: the gate asks, the answer carries counts only.
	v, perr := m.Call(context.Background(), "s", json.RawMessage(`{"op":"importChrome","replace":true}`))
	if perr != nil {
		t.Fatal(perr)
	}
	res := v.(ImportResult)
	if res.Profile.Dir != "Default" || res.Cookies.Found != 450 || res.Cookies.Imported != 449 || res.Failed != 1 || !res.Replaced || keys != 0 {
		t.Errorf("result %+v (key reads %d)", res, keys)
	}
	if len(g.asked) != 1 || !strings.Contains(g.asked[0], "Google Chrome sign-ins") {
		t.Errorf("gate %v", g.asked)
	}
	if got := f.callsLike("Storage.clearCookies"); len(got) != 1 {
		t.Errorf("replace %v", got)
	}
	if len(f.cookies) != 449 {
		t.Errorf("browser has %d cookies", len(f.cookies))
	}
	c := f.cookies[0]
	if c["domain"] != ".site0.test" || c["value"] != "secret-0" || c["sourceScheme"] != "Secure" || c["sameSite"] != "Lax" || c["sourcePort"] != float64(443) {
		t.Errorf("cookie %v", c)
	}
	data, _ := json.Marshal(v)
	all := string(data) + strings.Join(logged, "\n")
	if raw, err := os.ReadFile(status); err == nil {
		all += string(raw)
	} else {
		t.Error(err)
	}
	if strings.Contains(all, "secret-") {
		t.Errorf("a cookie value leaked: %s", all)
	}
	if last, err := m.LastImport(); err != nil || last == nil || last.Cookies.Imported != 449 {
		t.Errorf("last %v %v", last, err)
	}
	// Profiles and a declined gate.
	v, perr = m.Call(context.Background(), "s", json.RawMessage(`{"op":"chromeProfiles"}`))
	if perr != nil || len(v.([]chromeimport.Profile)) != 1 {
		t.Errorf("profiles %v %v", v, perr)
	}
	g.deny = true
	if _, perr := m.Call(context.Background(), "s", json.RawMessage(`{"op":"importChrome"}`)); perr == nil || perr.Code != protocol.ErrDeclined {
		t.Errorf("declined %v", perr)
	}
	if _, perr := m.Call(context.Background(), "s", json.RawMessage(`{"op":"importChrome","profile":"Nobody"}`)); perr == nil {
		t.Error("an unknown profile")
	}
	// No import configured.
	off := NewManager(Options{Browser: f})
	if _, err := off.ImportFromChrome(context.Background(), ImportOptions{}); err == nil {
		t.Error("import without configuration")
	}
}

type keyCounter struct{ n *int }

func keyCount(n *int) keyCounter { return keyCounter{n} }

func (k keyCounter) SafeStoragePassword(context.Context) ([]byte, error) {
	*k.n++
	return []byte("unused"), nil
}
