package fennecmobile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"fennec/internal/runta"
)

// The account and its runtimes are read in one round trip.
func TestFindSharedReadsAccountAndRuntimesTogether(t *testing.T) {
	both := make(chan struct{})
	var arrived sync.WaitGroup
	arrived.Add(2)
	go func() { arrived.Wait(); close(both) }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Done()
		select {
		case <-both:
		case <-time.After(2 * time.Second):
			http.Error(w, "not overlapped", 500)
			return
		}
		switch r.URL.Path {
		case "/v2/me":
			fmt.Fprint(w, `{"data":{"user_id":"me"}}`)
		default:
			fmt.Fprint(w, `{"data":[{"id":"r1","owner_user_id":"me","status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}},{"id":"r2","owner_user_id":"other","status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]}`)
		}
	}))
	defer server.Close()
	shared, err := findShared(context.Background(), &runta.Client{Endpoint: server.URL})
	if err != nil || shared.ID != "r1" {
		t.Fatal(shared.ID, err)
	}
}

func TestRememberedRuntime(t *testing.T) {
	s := &Sessions{directory: t.TempDir()}
	if s.rememberedRuntime() != "" {
		t.Fatal("remembered before any connection")
	}
	s.rememberRuntime("r1")
	if got := s.rememberedRuntime(); got != "r1" {
		t.Fatalf("remembered %q", got)
	}
}
