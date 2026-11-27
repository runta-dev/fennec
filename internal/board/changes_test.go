package board

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// FEN-193: the desktop window and the Mac's sync wake on the change stamp, so
// reading the board must never move it (or they would spin), and every write
// must.
func TestChangeStampMovesOnWritesOnly(t *testing.T) {
	s := testStore(t)
	p, err := s.CreateProject(Project{Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(p.ID, "me", "Hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	stamp := s.ChangeStamp()
	if stamp == "" {
		t.Fatal("no stamp after a write")
	}
	h := s.Handler()
	for _, path := range []string{"/v1/projects", "/v1/projects/" + p.ID + "/requests", fmt.Sprintf("/v1/projects/%s/requests/%d", p.ID, r.Number)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://board.fennec"+path, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
	}
	if _, err := s.Sync(Exchange{Seen: map[string]uint64{}}); err != nil {
		t.Fatal(err)
	}
	if got := s.ChangeStamp(); got != stamp {
		t.Fatalf("reads moved the stamp: %q -> %q", stamp, got)
	}
	if _, err := s.Reply(p.ID, r.Number, "me", "", "Done"); err != nil {
		t.Fatal(err)
	}
	if s.ChangeStamp() == stamp {
		t.Fatal("a reply did not move the stamp")
	}
}

func TestWaitChangeWakesOnWrite(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Work"})
	r, _ := s.CreateRequest(p.ID, "me", "Hello", "", "")
	stamp := s.ChangeStamp()

	start := time.Now()
	if got := s.WaitChange(context.Background(), stamp, 150*time.Millisecond); got != stamp {
		t.Fatal("woke without a change")
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("returned before the wait ran out")
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := s.Reply(p.ID, r.Number, "me", "", "Done"); err != nil {
			t.Error(err)
		}
	}()
	start = time.Now()
	if got := s.WaitChange(context.Background(), stamp, 5*time.Second); got == stamp {
		t.Fatal("missed the reply")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("woke %v after the reply", waited)
	}
}

// The cloud holds an idle /v1/sync until its board changes, then answers
// with the change; a peer that asks no wait is answered at once.
func TestSyncWaitAnswersOnChange(t *testing.T) {
	cloud := testStore(t)
	cloud.Role = "cloud"
	p, _ := cloud.CreateProject(Project{Name: "Work"})
	r, _ := cloud.CreateRequest(p.ID, "me", "Hello", "", "")
	first, err := cloud.Sync(Exchange{Seen: map[string]uint64{}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]uint64{}
	for _, e := range first.Events {
		seen[e.Origin] = max(seen[e.Origin], e.Clock)
	}
	h := cloud.Handler()
	post := func(in Exchange) (Exchange, time.Duration) {
		body, _ := json.Marshal(in)
		w := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://board.fennec/v1/sync", bytes.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("sync: %d %s", w.Code, w.Body)
		}
		var out Exchange
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out, time.Since(start)
	}

	if out, took := post(Exchange{Seen: seen}); len(out.Events) != 0 || took > 500*time.Millisecond {
		t.Fatalf("no-wait sync: %d events in %v", len(out.Events), took)
	}
	if out, took := post(Exchange{Seen: seen, Wait: 200}); len(out.Events) != 0 || took < 200*time.Millisecond {
		t.Fatalf("idle wait: %d events in %v", len(out.Events), took)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		if _, err := cloud.Reply(p.ID, r.Number, "me", "", "Done"); err != nil {
			t.Error(err)
		}
	}()
	out, took := post(Exchange{Seen: seen, Wait: 4000})
	if len(out.Events) == 0 {
		t.Fatal("the reply did not come back")
	}
	if took > time.Second {
		t.Fatalf("answered %v after the wait began", took)
	}
	// An exchange that already has something to send never waits.
	if out, took := post(Exchange{Seen: map[string]uint64{}, Wait: 4000}); len(out.Events) == 0 || took > 500*time.Millisecond {
		t.Fatalf("backlog: %d events in %v", len(out.Events), took)
	}
}
