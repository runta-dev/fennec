package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (Store, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, ".fen", "amber-cedar")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	return Store{Home: home}, workspace
}
func TestRegistrationSnapshotsAndScopes(t *testing.T) {
	store, workspace := fixture(t)
	file := filepath.Join(workspace, "report.txt")
	os.WriteFile(file, []byte("first"), 0600)
	first, err := store.Register("amber-cedar", file, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "document" || first.Name != "report.txt" || !RefPattern.MatchString(first.Ref) {
		t.Fatal(first)
	}
	same, err := store.Register("amber-cedar", file, "Report")
	if err != nil || same != first {
		t.Fatal(same, err)
	}
	os.WriteFile(file, []byte("second"), 0600)
	second, err := store.Register("amber-cedar", file, "Report")
	if err != nil || first.Ref == second.Ref {
		t.Fatal(second, err)
	}
	snapshot, _, err := store.Open("amber-cedar", first.Ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(snapshot)
	snapshot.Close()
	if string(raw) != "first" {
		t.Fatalf("snapshot changed: %q", raw)
	}
	if _, err := store.Metadata("other-place", first.Ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-session lookup: %v", err)
	}
	secret := filepath.Join(store.Home, "secret.txt")
	os.WriteFile(secret, []byte("secret"), 0600)
	os.Symlink(secret, filepath.Join(workspace, "escape.txt"))
	for _, source := range []string{secret, "../../secret.txt", "escape.txt", "."} {
		if _, err := store.Register("amber-cedar", source, ""); err == nil {
			t.Fatalf("registered %s", source)
		}
	}
	os.Symlink(workspace, filepath.Join(store.Home, ".fen", "linked-place"))
	if _, err := store.Register("linked-place", "report.txt", ""); err == nil {
		t.Fatal("linked workspace accepted")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			m, err := store.Register("amber-cedar", file, "Report")
			if err != nil || m.Ref != second.Ref {
				t.Errorf("concurrent register: %+v %v", m, err)
			}
		})
	}
	wg.Wait()
}
func TestUploadsIdempotentBoundedAndPrivate(t *testing.T) {
	store, workspace := fixture(t)
	id := strings.Repeat("a", 32)
	uploaded, err := store.UploadFile("amber-cedar", id, "data.bin", bytes.NewReader([]byte{0, 255, 10, 13}))
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.UploadFile("amber-cedar", id, "data.bin", bytes.NewReader([]byte{0, 255, 10, 13}))
	if err != nil || same != uploaded {
		t.Fatal(same, err)
	}
	if _, err = store.UploadFile("amber-cedar", id, "data.bin", strings.NewReader("replacement")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	content, err := os.ReadFile(uploaded.Path)
	if err != nil || !bytes.Equal(content, []byte{0, 255, 10, 13}) {
		t.Fatal(content, err)
	}
	for _, name := range []string{"../escape", ".receipt.json", "a/b", "a\x00b"} {
		if _, err = store.UploadFile("amber-cedar", strings.Repeat("b", 32), name, strings.NewReader("x")); err == nil {
			t.Fatal(name)
		}
	}
	if _, err = store.UploadFile("amber-cedar", strings.Repeat("c", 32), "partial.txt", failingReader{}); err == nil {
		t.Fatal("partial accepted")
	}
	entries, _ := os.ReadDir(filepath.Join(workspace, "uploads"))
	if len(entries) != 1 {
		t.Fatal("partial upload remains", entries)
	}
	large := filepath.Join(workspace, "large.bin")
	f, _ := os.Create(large)
	f.Truncate(MaxBytes + 1)
	f.Close()
	if _, err = store.Register("amber-cedar", large, ""); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(workspace, "evil.html"), []byte("<html><script>alert(1)</script>"), 0600)
	html, err := store.Register("amber-cedar", "evil.html", "")
	if err != nil || html.Kind != "download" {
		t.Fatal(html, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPrivateHTTPRangeAndAccess(t *testing.T) {
	store, workspace := fixture(t)
	os.WriteFile(filepath.Join(workspace, "text.txt"), []byte("0123456789"), 0600)
	metadata, err := store.Register("amber-cedar", "text.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := store.Handler()
	endpoint := "http://preview.fennec/v1/sessions/amber-cedar/previews/" + metadata.Ref + "/content"
	request := httptest.NewRequest("GET", endpoint, nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 206 || response.Body.String() != "2345" || response.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatal(response.Code, response.Body.String(), response.Header())
	}
	for _, url := range []string{strings.Replace(endpoint, "amber-cedar", "other-place", 1), "http://preview.fennec/v1/sessions/amber-cedar/previews/../secret/content"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", url, nil))
		if response.Code == 200 {
			t.Fatal(url)
		}
	}
	request = httptest.NewRequest("GET", endpoint, nil)
	request.Header.Set("Origin", "https://untrusted.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal(response.Code)
	}
	request = httptest.NewRequest("HEAD", endpoint, nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "10" {
		t.Fatal(response.Code, response.Header())
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing content isolation")
	}
}
func TestHTTPOverPrivatePipeAndUpload(t *testing.T) {
	store, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return store.Dial(ctx) }}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	endpoint := "http://preview.fennec/v1/sessions/amber-cedar/uploads/" + strings.Repeat("d", 32) + "?name=hello.txt"
	request, _ := http.NewRequestWithContext(ctx, "PUT", endpoint, strings.NewReader("private bytes"))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded Upload
	err = json.NewDecoder(response.Body).Decode(&uploaded)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || uploaded.Preview.Size != 13 {
		t.Fatal(uploaded, response.StatusCode, err)
	}
	request, _ = http.NewRequestWithContext(ctx, "GET", "http://preview.fennec/v1/sessions/amber-cedar/previews/"+uploaded.Preview.Ref+"/content", nil)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != "private bytes" {
		t.Fatal(string(data), err)
	}
}
