package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// slowDesk is the fake desk with a driver that takes its time: typing
// sends one character at a time, and a hit-checked click waits for click.
type slowDesk struct {
	*fakedesk.Desk
	mu     sync.Mutex
	typed  []rune
	click  chan struct{} // nil clicks at once
	inHit  chan struct{}
	pause  time.Duration
	sentAt []time.Time
}

func (s *slowDesk) Driver(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if tool == "type_text" {
		text, _ := args["text"].(string)
		for _, r := range text {
			s.mu.Lock()
			s.typed = append(s.typed, r)
			s.mu.Unlock()
			time.Sleep(s.pause)
		}
	}
	s.mu.Lock()
	s.sentAt = append(s.sentAt, time.Now())
	s.mu.Unlock()
	return s.Desk.Driver(ctx, tool, args)
}

func (s *slowDesk) HitClick(ctx context.Context, w view.RawWindow, n view.RawNode, count int) (json.RawMessage, error) {
	if s.inHit != nil {
		s.inHit <- struct{}{}
	}
	if s.click != nil {
		<-s.click
	}
	return s.Desk.HitClick(ctx, w, n, count)
}

func (s *slowDesk) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.typed)
}

func (s *slowDesk) sends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sentAt)
}

// actDesk is an engine whose act tool is the real one over a fake desk with
// a TextEdit window: a Save button and a focused Name field.
func actDesk(t *testing.T, busy time.Duration) (*served, *slowDesk) {
	t.Helper()
	d := fakedesk.New()
	d.Win("cg:10", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	d.Add("cg:10", "save", "button", "Save", fakedesk.Box(100, 100, 80, 20), "cg:10")
	d.Add("cg:10", "name", "textbox", "Name", fakedesk.Box(100, 200, 200, 20), "cg:10")
	d.Hits[[2]int{140, 110}] = fakedesk.Hit{Node: "save", Win: "cg:10"}
	d.Hits[[2]int{200, 210}] = fakedesk.Hit{Node: "name", Win: "cg:10"}
	d.Focus[100] = []*view.RawNode{d.Node("name")}
	s := &slowDesk{Desk: d}
	p, err := policy.New(policy.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Activity.Idle = func() time.Duration { return time.Hour }
	x := &actions.Executor{B: s, Refs: view.NewRegistry(0), Policy: p, Sleep: func(context.Context, time.Duration) error { return nil }}
	eng := newEngine(map[string]engine.Tool{protocol.ToolAct: engine.ActTool(x, p, nil)})
	eng.Turn.Busy = busy
	return eng, s
}

func act(eng *served, agent, args string) string {
	return eng.Call(context.Background(), session(agent), "act", json.RawMessage(args)).Text
}

func TestConcurrentTypingSerialised(t *testing.T) {
	eng, s := actDesk(t, 10*time.Second)
	s.pause = 3 * time.Millisecond
	var wg sync.WaitGroup
	for _, c := range []struct{ agent, text string }{{"a", "aaaaaaaaaaaaaaaaaaaa"}, {"b", "bbbbbbbbbbbbbbbbbbbb"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = act(eng, c.agent, `{"steps":[{"type":"`+c.text+`","window":"Notes"}]}`)
		}()
	}
	wg.Wait()
	got := s.text()
	if got != strings.Repeat("a", 20)+strings.Repeat("b", 20) && got != strings.Repeat("b", 20)+strings.Repeat("a", 20) {
		t.Errorf("typing interleaved: %q", got)
	}
}

func TestBusyTimeout(t *testing.T) {
	eng, s := actDesk(t, 200*time.Millisecond)
	s.pause = 15 * time.Millisecond
	long := strings.Repeat("x", 40) // about 600 ms of typing
	done := make(chan string, 1)
	go func() { done <- act(eng, "a", `{"steps":[{"type":"`+long+`","window":"Notes"}]}`) }()
	waitFor(t, func() bool { return len(s.text()) > 0 })
	before := s.sends()
	start := time.Now()
	text := act(eng, "b", `{"steps":[{"key":"cmd+s","window":"Notes"}]}`)
	if !strings.Contains(text, `"busy"`) || !strings.Contains(text, "nothing was sent") {
		t.Fatalf("a new action during typing: %s", text)
	}
	if took := time.Since(start); took < 200*time.Millisecond {
		t.Errorf("busy after %v", took)
	}
	<-done
	if s.text() != long {
		t.Errorf("the running action was cut: %q", s.text())
	}
	if n := s.sends(); n != before+1 {
		t.Errorf("%d driver calls after the busy one began; only the typing's own", n-before)
	}
	for _, c := range s.Calls {
		if c.Tool != "type_text" {
			t.Errorf("the busy action sent %s", c.Tool)
		}
	}
}

func TestElementClickHoldsTurn(t *testing.T) {
	eng, s := actDesk(t, 150*time.Millisecond)
	s.click = make(chan struct{})
	s.inHit = make(chan struct{}, 1)
	done := make(chan string, 1)
	go func() { done <- act(eng, "a", `{"steps":[{"click":"textbox \"Name\"","window":"Notes"}]}`) }()
	select {
	case <-s.inHit:
	case text := <-done:
		t.Fatalf("the element click did not reach the driver: %s", text)
	}
	// Until the driver has clicked, the turn stays with the click.
	if h := eng.Turn.Holder(); h != "a" {
		t.Errorf("holder %q during the driver's click", h)
	}
	if text := act(eng, "b", `{"steps":[{"key":"cmd+s","window":"Notes"}]}`); !strings.Contains(text, `"busy"`) {
		t.Errorf("another action during the click: %s", text)
	}
	close(s.click)
	<-done
	waitFor(t, func() bool { return eng.Turn.Holder() == "" })
	if len(s.HitCalls) != 1 || s.HitCalls[0].Node != "name" {
		t.Errorf("hit clicks %+v", s.HitCalls)
	}
}

// visionStep is a pixel click of the kind vision mode sends.
const visionStep = `{"steps":[{"click":{"point":{"x":140,"y":110}}}]}`

func TestVisionActWaitsForSharedTurn(t *testing.T) {
	eng, s := actDesk(t, 5*time.Second)
	release, err := eng.Turn.Acquire(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- act(eng, "vision-agent", visionStep) }()
	time.Sleep(100 * time.Millisecond)
	if n := s.sends() + len(s.HitCalls); n != 0 {
		t.Fatalf("a waiting vision act sent %d driver calls", n)
	}
	select {
	case text := <-done:
		t.Fatalf("answered while the turn was held: %s", text)
	default:
	}
	freed := time.Now()
	release()
	select {
	case text := <-done:
		if strings.Contains(text, `"busy"`) {
			t.Errorf("busy after the turn freed: %s", text)
		}
		if time.Since(freed) > time.Second {
			t.Errorf("proceeded %v after the turn freed", time.Since(freed))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the vision act never proceeded")
	}
	if n := s.sends() + len(s.HitCalls); n == 0 {
		t.Error("the vision act sent nothing once it held the turn")
	}
}

func TestVisionActBusyAfterTurnWait(t *testing.T) {
	eng, s := actDesk(t, 250*time.Millisecond)
	release, err := eng.Turn.Acquire(context.Background(), "act-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	text := act(eng, "vision-agent", visionStep)
	if !strings.Contains(text, `"busy"`) {
		t.Fatalf("vision act behind a held turn: %s", text)
	}
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Errorf("busy after %v", took)
	}
	if eng.Turn.Bound() != 250*time.Millisecond {
		t.Errorf("bound %v", eng.Turn.Bound())
	}
	if n := s.sends() + len(s.HitCalls); n != 0 {
		t.Errorf("a busy vision act sent %d driver calls", n)
	}
}
