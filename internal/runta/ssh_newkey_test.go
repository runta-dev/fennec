package runta

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A new device key and the runtime revision needed to attach it are read in
// one round trip: the POST and the GET must be in flight together.
func TestNewDeviceKeyCreationOverlapsRuntimeRead(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewPublicKey(public)
	host := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey)))
	var mu sync.Mutex
	var attached string
	both := make(chan struct{})
	var arrived sync.WaitGroup
	arrived.Add(2)
	go func() { arrived.Wait(); close(both) }()
	meet := func(w http.ResponseWriter) bool {
		arrived.Done()
		select {
		case <-both:
			return true
		case <-time.After(2 * time.Second):
			http.Error(w, "not overlapped", 500)
			return false
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v2/ssh-keys":
			if meet(w) {
				fmt.Fprint(w, `{"data":{"id":"k1"}}`)
			}
		case r.Method == "GET" && r.URL.Path == "/v2/runtimes/r":
			if meet(w) {
				fmt.Fprint(w, `{"data":{"id":"r","revision":7,"status":"running"}}`)
			}
		case r.Method == "PUT":
			mu.Lock()
			attached = r.URL.Path + "?" + r.URL.RawQuery
			mu.Unlock()
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/v2/ssh-host-key":
			fmt.Fprintf(w, `{"data":{"public_key":%q}}`, host)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := Client{Endpoint: server.URL}
	if _, err := client.prepareSSH(ctx, "r", t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attached != "/v2/runtimes/r/ssh-keys/k1?expected_revision=7" {
		t.Fatalf("attached %q", attached)
	}
}
