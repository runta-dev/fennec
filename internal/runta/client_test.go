package runta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
					VCPUs   int `json:"vcpus"`
					Memory  int `json:"memory_mib"`
					DiskGiB int `json:"disk_gib"`
				} `json:"requests"`
				Limits struct {
					VCPUs  int `json:"vcpus"`
					Memory int `json:"memory_mib"`
				} `json:"limits"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Resources.Requests.VCPUs != 4 || body.Resources.Limits.VCPUs != 8 || body.Resources.Requests.Memory != 4096 || body.Resources.Limits.Memory != 16384 || body.Resources.Requests.DiskGiB != 100 {
			t.Errorf("wrong default resources: %+v", body.Resources)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		fmt.Fprint(w, `{"data":{"id":"shared","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"},"resources":{"current":{"memory_mib":4096,"observed_disk_gib":16},"requests":{"vcpus":2,"memory_mib":4096,"disk_gib":16},"limits":{"memory_mib":16384}}}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	for i := 0; i < 2; i++ {
		got, err := c.EnsureShared(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.Resources == nil || got.Resources.Current.Memory != 4096 || got.Resources.Requests.Memory != 4096 || got.Resources.Limits.Memory != 16384 {
			t.Fatalf("unexpected decoded runtime resources: %+v", got.Resources)
		}
	}
	legacy := sha256.Sum256([]byte("fennec/shared-v1/mine"))
	if len(keys) > 0 && keys[0] == "fennec-"+hex.EncodeToString(legacy[:]) {
		t.Fatal("new shape reused old creation key")
	}
	fixed4gIntent, _ := json.Marshal(map[string]any{
		"name":                  "Fennec",
		"environment_variables": map[string]string{"FENNEC_PURPOSE": "shared-v1"},
		"resources": map[string]any{
			"requests": map[string]any{"vcpus": 2, "memory_mib": 4096},
			"limits":   map[string]any{"memory_mib": 4096},
		},
	})
	fixed4gDigest := sha256.Sum256(append([]byte("fennec/shared-v1/mine\x00"), fixed4gIntent...))
	if len(keys) > 0 && keys[0] == "fennec-"+hex.EncodeToString(fixed4gDigest[:]) {
		t.Fatal("autoscaling 4g-16g shape collided with fixed 4g idempotency key")
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatal(keys)
	}
}

// An API that predates dynamic vCPU rejects limits.vcpus; creation must fall
// back to a fixed 4 vCPU runtime instead of failing.
func TestCreateFallsBackWithoutDynamicVCPU(t *testing.T) {
	var keys []string
	var vcpuLimits []bool
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
				Requests map[string]any `json:"requests"`
				Limits   map[string]any `json:"limits"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_, hasLimit := body.Resources.Limits["vcpus"]
		vcpuLimits = append(vcpuLimits, hasLimit)
		if hasLimit {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":{"code":"invalid_request","message":"unknown field vcpus"}}`)
			return
		}
		if body.Resources.Requests["vcpus"] != float64(4) || body.Resources.Requests["disk_gib"] != float64(100) || body.Resources.Limits["memory_mib"] != float64(16384) {
			t.Errorf("unexpected fallback resources: %+v", body.Resources)
		}
		fmt.Fprint(w, `{"data":{"id":"shared","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"},"resources":{"current":{"vcpus":4,"memory_mib":4096,"observed_disk_gib":1},"requests":{"vcpus":4,"memory_mib":4096,"disk_gib":100},"limits":{"memory_mib":16384}}}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	got, err := c.EnsureShared(context.Background())
	if err != nil || got.ID != "shared" || got.Resources.Requests.VCPUs != 4 {
		t.Fatalf("%+v %v", got, err)
	}
	if len(keys) != 2 || !vcpuLimits[0] || vcpuLimits[1] || keys[0] == keys[1] {
		t.Fatalf("expected dynamic attempt then fixed fallback with distinct keys: %v %v", vcpuLimits, keys)
	}
}

// Non-validation failures must not trigger a fallback POST.
func TestCreateDoesNotFallBackOnServerError(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/me":
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
		case r.Method == "GET":
			fmt.Fprint(w, `{"data":[],"pagination":{"has_more":false}}`)
		default:
			posts++
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	var apiError *APIError
	if _, err := c.EnsureShared(context.Background()); !errors.As(err, &apiError) || apiError.Status != http.StatusServiceUnavailable || posts != 1 {
		t.Fatalf("err=%v posts=%d", err, posts)
	}
}

func TestEnsureSharedReconciles2c16gOrFixed2c4gToDynamic4g16g(t *testing.T) {
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/me":
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
		case r.Method == "GET" && r.URL.Path == "/v2/runtimes":
			fmt.Fprint(w, `{"data":[{"id":"rt-1","owner_user_id":"mine","revision":5,"status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"},"resources":{"current":{"memory_mib":16384,"observed_disk_gib":16},"requests":{"vcpus":2,"memory_mib":16384,"disk_gib":16},"limits":{"memory_mib":16384}}}],"pagination":{"has_more":false}}`)
		case r.Method == "PATCH" && r.URL.Path == "/v2/runtimes/rt-1":
			patches++
			if r.URL.Query().Get("expected_revision") != "5" {
				t.Fatalf("expected revision 5, got %s", r.URL.RawQuery)
			}
			var body struct {
				Resources struct {
					Requests struct {
						Memory uint64 `json:"memory_mib"`
					} `json:"requests"`
					Limits struct {
						Memory uint64 `json:"memory_mib"`
					} `json:"limits"`
				} `json:"resources"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Resources.Requests.Memory != 4096 || body.Resources.Limits.Memory != 16384 {
				t.Fatalf("unexpected patch resources: %+v", body.Resources)
			}
			fmt.Fprint(w, `{"data":{"id":"rt-1","owner_user_id":"mine","revision":6,"status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"},"resources":{"current":{"memory_mib":4096,"observed_disk_gib":16},"requests":{"vcpus":2,"memory_mib":4096,"disk_gib":16},"limits":{"memory_mib":16384}}}}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	got, err := c.EnsureShared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if patches != 1 || got.Resources == nil || got.Resources.Requests.Memory != 4096 || got.Resources.Limits.Memory != 16384 {
		t.Fatalf("expected 2c16g patched down to 2c(4g-16g), patches=%d got=%+v", patches, got.Resources)
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

func TestSharedDeletionPolicyRunsBeforeCreation(t *testing.T) {
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
			t.Error("blocked cloud was mutated")
		}
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"owner"}}`)
			return
		}
		fmt.Fprint(w, `{"data":[],"pagination":{"has_more":false}}`)
	}))
	defer server.Close()
	denied := fmt.Errorf("intentional cloud deletion")
	c := Client{Endpoint: server.URL, Token: "fixture", SharedAllowed: func(owner string) error {
		if owner != "owner" {
			t.Error("unverified owner")
		}
		return denied
	}}
	if _, err := c.EnsureShared(context.Background()); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if mutations.Load() != 0 {
		t.Fatal("cloud recreated after deletion")
	}
}

func TestRuntimeGoneOnlyOnAuthoritativeAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/runtimes/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/v2/runtimes/deleted", "/v2/runtimes/deleting", "/v2/runtimes/running", "/v2/runtimes/failed":
			id := r.URL.Path[len("/v2/runtimes/"):]
			fmt.Fprintf(w, `{"data":{"id":%q,"status":%q}}`, id, id)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	for id, want := range map[string]bool{"missing": true, "deleted": true, "deleting": true, "running": false, "failed": false} {
		if gone, err := c.RuntimeGone(context.Background(), id); err != nil || gone != want {
			t.Errorf("%s: gone=%v err=%v", id, gone, err)
		}
	}
	if gone, err := c.RuntimeGone(context.Background(), "outage"); err == nil || gone {
		t.Fatalf("an outage must not look like a deletion: gone=%v err=%v", gone, err)
	}
}

// A creation key replayed for a runtime deleted since must not hand the
// deleted runtime back as the shared computer.
func TestCreateReplayOfDeletedRuntimeUsesFreshKey(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/me":
			fmt.Fprint(w, `{"data":{"user_id":"mine"}}`)
		case r.Method == "GET":
			fmt.Fprint(w, `{"data":[],"pagination":{"has_more":false}}`)
		default:
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if len(keys) == 1 {
				fmt.Fprint(w, `{"data":{"id":"gone","status":"deleted","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
				return
			}
			fmt.Fprint(w, `{"data":{"id":"fresh","status":"provisioning","owner_user_id":"mine","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
		}
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	got, err := c.EnsureShared(context.Background())
	if err != nil || got.ID != "fresh" {
		t.Fatalf("%+v %v", got, err)
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("expected a second creation under a fresh key: %v", keys)
	}
}
