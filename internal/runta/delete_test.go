package runta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeleteOwnedSharedWaitsForRemoval(t *testing.T) {
	deleted := false
	polls := 0
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"owner"}}`)
			return
		}
		if r.Method == "DELETE" {
			deletes++
			if r.URL.Query().Get("expected_revision") != "7" {
				t.Error("revision omitted")
			}
			deleted = true
			w.WriteHeader(202)
			return
		}
		if deleted {
			polls++
			if polls == 2 {
				w.WriteHeader(404)
				return
			}
		}
		status := "running"
		if deleted {
			status = "deleting"
		}
		fmt.Fprintf(w, `{"data":{"id":"runtime","owner_user_id":"owner","revision":7,"status":%q,"environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`, status)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL, PollInterval: time.Millisecond}
	if e := c.DeleteOwnedShared(context.Background(), "runtime", "owner", 7); e != nil {
		t.Fatal(e)
	}
	if deletes != 1 || polls != 2 {
		t.Fatal(deletes, polls)
	}
}
func TestDeleteRejectsChangedOwnerPurposeAndRevision(t *testing.T) {
	for _, fixture := range []struct {
		user, owner, purpose string
		revision             int
	}{{"other", "owner", "shared-v1", 7}, {"owner", "other", "shared-v1", 7}, {"owner", "owner", "other-app", 7}, {"owner", "owner", "shared-v1", 8}} {
		deletes := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				deletes++
				return
			}
			if r.URL.Path == "/v2/me" {
				fmt.Fprintf(w, `{"data":{"user_id":%q}}`, fixture.user)
				return
			}
			fmt.Fprintf(w, `{"data":{"id":"runtime","owner_user_id":%q,"revision":%d,"status":"running","environment_variables":{"FENNEC_PURPOSE":%q}}}`, fixture.owner, fixture.revision, fixture.purpose)
		}))
		c := Client{Endpoint: server.URL}
		e := c.DeleteOwnedShared(context.Background(), "runtime", "owner", 7)
		server.Close()
		if e == nil || deletes != 0 {
			t.Fatal("unsafe deletion", e, deletes)
		}
	}
}
func TestAcceptedDeletionTimeoutIsNotSuccess(t *testing.T) {
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"owner"}}`)
			return
		}
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(202)
			return
		}
		fmt.Fprint(w, `{"data":{"id":"runtime","owner_user_id":"owner","revision":7,"status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := Client{Endpoint: server.URL, PollInterval: time.Millisecond}
	if e := c.DeleteOwnedShared(ctx, "runtime", "owner", 7); e == nil || deletes != 1 {
		t.Fatal("accepted deletion mistaken for completion", e, deletes)
	}
}
func TestRejectedDeletionDoesNotDiscardLocalRecovery(t *testing.T) {
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			fmt.Fprint(w, `{"data":{"user_id":"owner"}}`)
			return
		}
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(403)
			return
		}
		if deletes > 0 {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"data":{"id":"runtime","owner_user_id":"owner","revision":7,"status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	if e := c.DeleteOwnedShared(context.Background(), "runtime", "owner", 7); e == nil {
		t.Fatal("authorization rejection treated as success")
	}
}

func TestDeletionConflictIsDistinctFromUnknownOutcome(t *testing.T) {
	for _, status := range []int{409, 412, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/me" {
					fmt.Fprint(w, `{"data":{"user_id":"owner"}}`)
					return
				}
				if r.Method == "DELETE" {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{"data":{"id":"runtime","owner_user_id":"owner","revision":7,"status":"running","environment_variables":{"FENNEC_PURPOSE":"shared-v1"}}}`)
			}))
			defer server.Close()
			c := Client{Endpoint: server.URL}
			err := c.DeleteOwnedShared(context.Background(), "runtime", "owner", 7)
			if err == nil {
				t.Fatal("rejection accepted")
			}
			if errors.Is(err, ErrDeletionRejected) != (status == 409 || status == 412) {
				t.Fatal("incorrect outcome classification", status, err)
			}
		})
	}
}
