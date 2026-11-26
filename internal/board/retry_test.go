package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// An unconfirmed run waits for an explicit retry: it goes back to the queue
// with its slot freed, and the old session can no longer complete it.
func TestRetryRequeuesUnconfirmedRequest(t *testing.T) {
	store, p, r := unconfirmedRun(t)
	got, err := store.Retry(p.ID, r.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Queued || got.Failure != "" || got.Session != "" || got.RunLocation != "" || got.Offer != nil {
		t.Fatalf("not requeued: %+v", got)
	}
	if q, _ := store.Queue(); len(q) != 1 || q[0].Working != 0 || len(q[0].Queued) != 1 {
		t.Fatalf("queue = %+v", q)
	}
	if _, err := store.Complete(p.ID, r.Number, "calm-fox", "claude", "late", Resolved); !errors.Is(err, ErrConflict) {
		t.Fatal("old session completed the retried request", err)
	}
	if _, err := store.Retry(p.ID, r.Number); !errors.Is(err, ErrConflict) {
		t.Fatal("retried a queued request", err)
	}
	if _, err := store.Claim(p.ID, r.Number, "new-run", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Retry(p.ID, r.Number); !errors.Is(err, ErrConflict) {
		t.Fatal("retried a running request", err)
	}
}

func TestAgentCommandListsAndRetries(t *testing.T) {
	store, p, r := unconfirmedRun(t)
	var out bytes.Buffer
	if err := AgentCommand(store.Home, []string{"list", p.ID}, nil, &out); err != nil {
		t.Fatal(err)
	}
	var items []QueueItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("list = %s (%v)", out.Bytes(), err)
	}
	if it := items[0]; it.Ref != p.Key+"-"+strconv.Itoa(r.Number) || it.Status != Unknown || it.Session != "calm-fox" || it.Failure == "" {
		t.Fatalf("item = %+v", it)
	}
	out.Reset()
	if err := AgentCommand(store.Home, []string{"retry", p.ID, strconv.Itoa(r.Number)}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Request(p.ID, r.Number); got.Status != Queued {
		t.Fatalf("status = %s", got.Status)
	}
	err := AgentCommand(store.Home, []string{"retry", p.ID, strconv.Itoa(r.Number)}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "is queued") {
		t.Fatalf("retrying a queued request: %v", err)
	}
	if err := AgentCommand(store.Home, []string{"retry", p.ID, "99"}, nil, &out); err == nil {
		t.Fatal("retried a missing request")
	}
}
