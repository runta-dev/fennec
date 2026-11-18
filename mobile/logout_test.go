package fennecmobile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRevokeTokenDeletesTheCurrentKey(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	revokeToken(context.Background(), server.URL+"/", "runta-key")
	if got != "DELETE /v2/auth/token Bearer runta-key" {
		t.Fatalf("request %q", got)
	}
	// An unreachable server must not block sign-out.
	server.Close()
	revokeToken(context.Background(), server.URL, "runta-key")
}
