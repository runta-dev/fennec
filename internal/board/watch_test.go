package board

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

type watchEvent struct {
	Type     string            `json:"type"`
	Project  string            `json:"project"`
	Reset    bool              `json:"reset"`
	Requests []Summary         `json:"requests"`
	Order    []int             `json:"order"`
	Changed  []Summary         `json:"changed"`
	Projects []json.RawMessage `json:"projects"`
	Number   int               `json:"number"`
	Thread   *Request          `json:"thread"`
	Error    string            `json:"error"`
}

func TestWatchPushesOnlyWhatChanged(t *testing.T) {
	tick, rescan := WatchTick, WatchRescan
	WatchTick, WatchRescan = 10*time.Millisecond, time.Hour
	defer func() { WatchTick, WatchRescan = tick, rescan }()

	home := t.TempDir()
	s := Store{Home: home}
	p, err := s.CreateProject(Project{Name: "Watch"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateRequest(p.ID, "me", "First", "", "one")
	if err != nil {
		t.Fatal(err)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, home, inR, outW); outW.Close() }()
	events := bufio.NewScanner(outR)
	events.Buffer(nil, 1<<20)
	next := func() watchEvent {
		t.Helper()
		got := make(chan watchEvent, 1)
		go func() {
			var e watchEvent
			if events.Scan() {
				json.Unmarshal(events.Bytes(), &e)
			}
			got <- e
		}()
		select {
		case e := <-got:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
			return watchEvent{}
		}
	}
	subscribe := func(project string, thread int) {
		line, _ := json.Marshal(watchSub{Project: project, Thread: thread})
		inW.Write(append(line, '\n'))
	}

	if e := next(); e.Type != "projects" || len(e.Projects) != 1 {
		t.Fatalf("first event = %+v, want the projects", e)
	}
	subscribe(p.ID, 0)
	if e := next(); e.Type != "requests" || !e.Reset || len(e.Requests) != 1 || e.Requests[0].Title != "First" {
		t.Fatalf("after subscribing: %+v, want the whole list", e)
	}

	// A new request: only that row, plus the order; the counts in the
	// project list change too.
	second, err := s.CreateRequest(p.ID, "me", "Second", "", "two")
	if err != nil {
		t.Fatal(err)
	}
	var delta watchEvent
	for i := 0; i < 2; i++ {
		if e := next(); e.Type == "requests" {
			delta = e
		}
	}
	if delta.Reset || len(delta.Changed) != 1 || delta.Changed[0].Number != second.Number || len(delta.Order) != 2 {
		t.Fatalf("after a new request: %+v, want one changed row and two numbers", delta)
	}

	// Following a thread sends it, and a reply sends it again.
	subscribe(p.ID, first.Number)
	if e := next(); e.Type != "thread" || e.Thread == nil || e.Thread.Title != "First" {
		t.Fatalf("after following a thread: %+v", e)
	}
	if _, err := s.Reply(p.ID, first.Number, "me", "", "more"); err != nil {
		t.Fatal(err)
	}
	sawThread := false
	for i := 0; i < 3 && !sawThread; i++ {
		e := next()
		sawThread = e.Type == "thread" && e.Thread != nil && len(e.Thread.Messages) == 2
	}
	if !sawThread {
		t.Fatal("the reply did not send the thread again")
	}

	// Closing standard input (the phone hung up) ends it.
	inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch kept running after its input closed")
	}
}
