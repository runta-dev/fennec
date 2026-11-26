package runta_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"fennec/internal/cloudstate"
	"fennec/internal/runta"
)

func TestRetiredCreationRecoversAndPersistsAcrossLostResponse(t *testing.T) {
	for _, message := range []string{
		"idempotency key was already consumed by a deleted runtime",
		"idempotency key was consumed by a deleted runtime",
		"runtime already exists: idempotency key was already consumed by a deleted runtime",
		"runtime already exists: idempotency key was consumed by a deleted runtime",
	} {
		t.Run(message, func(t *testing.T) {
			home := t.TempDir()
			var keys []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/me" {
					fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
					return
				}
				if r.Method == "GET" {
					fmt.Fprint(w, `{"data":[]}`)
					return
				}
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if len(keys) == 1 {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "already_exists", "message": message}})
					return
				}
				// The new identity must already be durable when the POST arrives.
				if cloudstate.NewScoped(home, "production").Generation("mine") == "" {
					t.Error("replacement POST preceded durable generation")
				}
				if len(keys) == 2 {
					// Simulate a gateway failure after creation; the list may lag too.
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				fmt.Fprint(w, `{"data":{"id":"replacement","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
			}))
			defer server.Close()
			newClient := func() *runta.Client {
				store := cloudstate.NewScoped(home, "production")
				return &runta.Client{Endpoint: server.URL, SharedAllowed: store.Check, CreationGeneration: store.Generation, RetireCreation: store.RecordRetiredCreation}
			}
			if _, err := newClient().EnsureShared(context.Background()); err == nil {
				t.Fatal("lost response must remain an uncertain outcome")
			}
			got, err := newClient().EnsureShared(context.Background())
			if err != nil || got.ID != "replacement" {
				t.Fatalf("runtime=%+v error=%v", got, err)
			}
			if len(keys) != 3 || keys[0] == keys[1] || keys[1] != keys[2] {
				t.Fatalf("replacement retry changed its identity: %v", keys)
			}
			if cloudstate.NewScoped(home, "integration").Generation("mine") != "" || cloudstate.NewScoped(home, "production").Generation("other") != "" {
				t.Fatal("generation leaked across environments or accounts")
			}
		})
	}
}

func TestCreationRecoveryRequiresDefinitiveRetirementAndSuccessfulReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, code, message, items string
		status, listStatus         int
		persistFailure, fenced     bool
		wantPosts, wantSaves       int
		wantID                     string
	}{
		{name: "generic conflict", status: 409, code: "already_exists", message: "runtime identity or name already exists", wantPosts: 1},
		{name: "input conflict", status: 409, code: "already_exists", message: "idempotency key was already used with different input", wantPosts: 1},
		{name: "wrong code", status: 409, code: "failed_precondition", message: "idempotency key was consumed by a deleted runtime", wantPosts: 1},
		{name: "server error", status: 503, code: "already_exists", message: "idempotency key was consumed by a deleted runtime", wantPosts: 1},
		{name: "list unavailable", status: 409, listStatus: 503, wantPosts: 1},
		{name: "existing machine", status: 409, items: `[{"id":"existing","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]`, wantPosts: 1, wantID: "existing"},
		{name: "ambiguous machines", status: 409, items: `[{"id":"a","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}},{"id":"b","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]`, wantPosts: 1},
		{name: "cannot persist", status: 409, persistFailure: true, wantPosts: 1, wantSaves: 1},
		{name: "intentional deletion", status: 409, fenced: true, wantPosts: 1, wantSaves: 1},
		{name: "bounded retry", status: 409, wantPosts: 2, wantSaves: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := cloudstate.New(t.TempDir())
			posts, saves, lists := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/me" {
					fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
					return
				}
				if r.Method == "GET" {
					lists++
					if lists > 1 && tc.listStatus != 0 {
						w.WriteHeader(tc.listStatus)
						return
					}
					items := "[]"
					if lists > 1 && tc.items != "" {
						items = tc.items
					}
					fmt.Fprintf(w, `{"data":%s}`, items)
					return
				}
				posts++
				if tc.fenced {
					if err := store.Begin(cloudstate.Record{Owner: "mine", Runtime: "old", Request: "delete"}); err != nil {
						t.Error(err)
					}
				}
				code, message := tc.code, tc.message
				if code == "" {
					code = "already_exists"
				}
				if message == "" {
					message = "idempotency key was consumed by a deleted runtime"
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
			}))
			defer server.Close()
			client := &runta.Client{Endpoint: server.URL, SharedAllowed: store.Check, CreationGeneration: store.Generation,
				RetireCreation: func(owner, generation string) error {
					saves++
					if tc.persistFailure {
						return os.ErrPermission
					}
					return store.RecordRetiredCreation(owner, generation)
				},
			}
			got, err := client.EnsureShared(context.Background())
			if tc.wantID == "" && err == nil || tc.wantID != "" && (err != nil || got.ID != tc.wantID) {
				t.Fatalf("runtime=%+v error=%v", got, err)
			}
			if tc.fenced && !errors.Is(err, cloudstate.ErrDisabled) {
				t.Fatalf("lost deletion guard: %v", err)
			}
			if posts != tc.wantPosts || saves != tc.wantSaves {
				t.Fatalf("posts=%d saves=%d", posts, saves)
			}
		})
	}
}

func TestRetiredCreationConvergesAcrossDevices(t *testing.T) {
	var mu sync.Mutex
	var oldKey string
	replacementKeys := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		key := r.Header.Get("Idempotency-Key")
		if oldKey == "" {
			oldKey = key
		}
		if key == oldKey {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":"already_exists","message":"runtime already exists: idempotency key was consumed by a deleted runtime"}}`)
			return
		}
		replacementKeys[key] = true
		fmt.Fprint(w, `{"data":{"id":"replacement","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
	}))
	defer server.Close()
	var devices sync.WaitGroup
	for range 2 {
		store := cloudstate.New(t.TempDir())
		devices.Go(func() {
			client := &runta.Client{Endpoint: server.URL, SharedAllowed: store.Check, CreationGeneration: store.Generation, RetireCreation: store.RecordRetiredCreation}
			got, err := client.EnsureShared(context.Background())
			if err != nil || got.ID != "replacement" {
				t.Errorf("runtime=%+v error=%v", got, err)
			}
		})
	}
	devices.Wait()
	if len(replacementKeys) != 1 {
		t.Fatalf("devices used different replacement identities: %v", replacementKeys)
	}
}
