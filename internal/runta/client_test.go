package runta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSharedRuntimeOwnershipAndPagination(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		switch r.URL.Path {
		case "/v2/me":
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
		case "/v2/runtimes":
			if r.Method == "POST" {
				posts++
				t.Error("must reuse owned runtime")
			}
			if r.URL.Query().Get("after") == "" {
				fmt.Fprint(w, `{"data":[{"id":"other","owner_user_id":"other","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}],"pagination":{"has_more":true,"next_cursor":"next"}}`)
			} else {
				fmt.Fprint(w, `{"data":[{"id":"ours","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}],"pagination":{"has_more":false}}`)
			}
		}
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL, Token: "secret"}
	r, err := c.EnsureShared(context.Background())
	if err != nil || r.ID != "ours" || posts != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestCreateStableIdempotency(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"data":[],"pagination":{"has_more":false}}`)
			return
		}
		var body struct {
			Resources struct {
				Requests struct {
					VCPUs  int `json:"vcpus"`
					Memory int `json:"memory_mib"`
				} `json:"requests"`
				Limits struct {
					Memory int `json:"memory_mib"`
				} `json:"limits"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Resources.Requests.VCPUs != 2 || body.Resources.Requests.Memory != 4096 || body.Resources.Limits.Memory != 4096 {
			t.Errorf("wrong default resources: %+v", body.Resources)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		fmt.Fprint(w, `{"data":{"id":"shared","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	for i := 0; i < 2; i++ {
		if _, err := c.EnsureShared(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	legacy := sha256.Sum256([]byte("fennec/shared-v1/mine"))
	if len(keys) > 0 && keys[0] == "fennec-"+hex.EncodeToString(legacy[:]) {
		t.Fatal("new shape reused old creation key")
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatal(keys)
	}
}
func TestReadyRequiresSSHAndHealthyRuntime(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprintf(w, `{"data":{"id":"r","status":"running","ssh_enabled":%t,"degraded":false}}`, calls > 1)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL, PollInterval: time.Millisecond}
	if err := c.WaitReady(context.Background(), "r", nil); err != nil || calls != 2 {
		t.Fatalf("%d %v", calls, err)
	}
}

func TestSharedRuntimeRejectsAmbiguousOrMalformedCandidates(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"id":"a","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}},{"id":"b","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]}`,
		`{"data":[{"owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				t.Error("unexpected create")
			}
			if r.URL.Path == "/v2/me" {
				fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
			} else {
				fmt.Fprint(w, body)
			}
		}))
		c := Client{Endpoint: server.URL}
		if _, err := c.EnsureShared(context.Background()); err == nil {
			t.Error("invalid candidates accepted")
		}
		server.Close()
	}
}
func TestSharedRuntimeRejectsCreateWithoutPurpose(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"data":[{"id":"unrelated","name":"Fennec","owner_user_id":"mine"}]}`)
			return
		}
		posts++
		fmt.Fprint(w, `{"data":{"id":"created","owner_user_id":"mine"}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	if _, err := c.EnsureShared(context.Background()); err == nil || posts != 1 {
		t.Fatal("name-only reuse or unmarked create accepted", err, posts)
	}
}

func TestCreateVerifiesOmittedEnvironmentByReadback(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
			return
		}
		if r.URL.Path == "/v2/runtimes/created" {
			reads++
			fmt.Fprint(w, `{"data":{"id":"created","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"id":"created","owner_user_id":"mine"}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	runtime, err := c.EnsureShared(context.Background())
	if err != nil || runtime.ID != "created" || reads != 1 {
		t.Fatal(runtime, reads, err)
	}
}

func TestWrongRuntimeReadbackCannotStartOrReportReady(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes++
		}
		fmt.Fprint(w, `{"data":{"id":"other","status":"running","ssh_enabled":true,"degraded":false,"revision":7}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	if err := c.Start(context.Background(), "expected"); err == nil {
		t.Fatal("started mismatched runtime")
	}
	if err := c.Resume(context.Background(), "expected"); err == nil {
		t.Fatal("resumed mismatched runtime")
	}
	emitted := false
	if err := c.WaitReady(context.Background(), "expected", func(Runtime) { emitted = true }); err == nil || emitted {
		t.Fatal("reported mismatched runtime ready")
	}
	if writes != 0 {
		t.Fatal("mutated runtime despite invalid readback")
	}
}

func TestCreateConflictReconcilesWithoutSecondCreate(t *testing.T) {
	for _, tc := range []struct {
		name, items string
		want        bool
	}{
		{"same-owner", `[{"id":"existing","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]`, true},
		{"other-owner", `[{"id":"existing","owner_user_id":"other","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]`, false},
		{"deleted", `[]`, false},
		{"ambiguous", `[{"id":"a","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}},{"id":"b","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, lists := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/me" {
					fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
					return
				}
				if r.Method == "POST" {
					posts++
					w.WriteHeader(http.StatusConflict)
					return
				}
				lists++
				items := `[]`
				if lists > 1 {
					items = tc.items
				}
				fmt.Fprintf(w, `{"data":%s}`, items)
			}))
			defer server.Close()
			client := Client{Endpoint: server.URL}
			got, err := client.EnsureShared(context.Background())
			if (err == nil) != tc.want {
				t.Fatalf("runtime=%+v err=%v", got, err)
			}
			if tc.want && got.ID != "existing" {
				t.Fatal(got)
			}
			if posts != 1 || lists != 2 {
				t.Fatalf("posts=%d lists=%d", posts, lists)
			}
		})
	}
}
