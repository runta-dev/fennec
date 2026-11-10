package runta

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnedSSHReadsOverlapAndRejectBeforeMutation(t *testing.T) {
	var reads atomic.Int32
	var writes atomic.Int32
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			http.Error(w, "unexpected mutation", 500)
			return
		}
		if reads.Add(1) == 5 {
			close(ready)
		}
		select {
		case <-ready:
		case <-r.Context().Done():
			return
		}
		switch r.URL.Path {
		case "/v2/me":
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
		case "/v2/runtimes/r":
			fmt.Fprint(w, `{"data":{"id":"r","owner_user_id":"other","status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
		case "/v2/ssh-host-key":
			fmt.Fprint(w, `{"data":{"public_key":"invalid-unused-key"}}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := Client{Endpoint: server.URL}
	_, err := client.PrepareOwnedSSH(ctx, "r", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ownership mismatch") || reads.Load() != 5 || writes.Load() != 0 {
		t.Fatal(err, reads.Load(), writes.Load())
	}
}

func TestOwnedRuntimeRequestsOverlapAndStillValidateOwnership(t *testing.T) {
	for _, owner := range []string{"mine", "other"} {
		t.Run(owner, func(t *testing.T) {
			identityStarted, runtimeStarted := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/me" {
					close(identityStarted)
					select {
					case <-runtimeStarted:
					case <-r.Context().Done():
						return
					}
					fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
				} else {
					close(runtimeStarted)
					select {
					case <-identityStarted:
					case <-r.Context().Done():
						return
					}
					fmt.Fprintf(w, `{"data":{"id":"r","owner_user_id":%q,"status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`, owner)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			client := Client{Endpoint: server.URL}
			value, err := client.OwnedRunningRuntime(ctx, "r")
			if owner == "mine" && (err != nil || value.ID != "r") {
				t.Fatal(value, err)
			}
			if owner == "other" && (err == nil || !strings.Contains(err.Error(), "ownership mismatch")) {
				t.Fatal("cross-account runtime accepted", err)
			}
		})
	}
}
