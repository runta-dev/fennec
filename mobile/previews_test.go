package fennecmobile

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewDownloadVerifiesBeforeReplacingDestination(t *testing.T) {
	for _, tc := range []struct {
		name, body, etag string
		size             int64
		valid            bool
	}{
		{"valid", "report", fmt.Sprintf(`"%x"`, sha256.Sum256([]byte("report"))), 6, true},
		{"truncated", "rep", fmt.Sprintf(`"%x"`, sha256.Sum256([]byte("report"))), 6, false},
		{"corrupt", "REPORT", fmt.Sprintf(`"%x"`, sha256.Sum256([]byte("report"))), 6, false},
		{"unknown-length", "report", "", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "report.txt")
			if err := os.WriteFile(destination, []byte("previous"), 0600); err != nil {
				t.Fatal(err)
			}
			response := &http.Response{ContentLength: tc.size, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
			response.Header.Set("ETag", tc.etag)
			err := savePreview(response, destination)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			want := "previous"
			if tc.valid {
				want = tc.body
			}
			if string(got) != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("partial download leaked: %v %v", entries, err)
			}
		})
	}
}
