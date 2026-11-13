package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// peer is the browser end of a fake pipe: it reads NUL-framed commands and
// answers with handler's replies.
type peer struct {
	in  *io.PipeReader // commands from the client
	out *io.PipeWriter // replies to the client
}

func pipePair() (*Conn, *peer) {
	cmdR, cmdW := io.Pipe()
	repR, repW := io.Pipe()
	c := New(repR, cmdW, func() { cmdW.Close(); repR.Close() })
	return c, &peer{in: cmdR, out: repW}
}

func (p *peer) serve(t *testing.T, handle func(m message) []message) {
	rd := bufio.NewReader(p.in)
	for {
		frame, err := rd.ReadBytes(0)
		if err != nil {
			return
		}
		var m message
		if err := json.Unmarshal(frame[:len(frame)-1], &m); err != nil {
			t.Errorf("bad frame %q", frame)
			return
		}
		for _, r := range handle(m) {
			data, _ := json.Marshal(r)
			if _, err := p.out.Write(append(data, 0)); err != nil {
				return
			}
		}
	}
}

func TestFramingAndSessions(t *testing.T) {
	c, p := pipePair()
	defer c.Close()
	big := strings.Repeat("x", 200000) // more than the reader's buffer
	go p.serve(t, func(m message) []message {
		switch m.Method {
		case "Browser.getVersion":
			return []message{
				{Method: "Target.targetCreated", Params: json.RawMessage(`{"a":1}`)},
				{ID: m.ID, Result: json.RawMessage(`{"product":"Chrome/1"}`)},
			}
		case "Runtime.evaluate":
			if m.SessionID != "S1" {
				return []message{{ID: m.ID, Error: &Error{Code: -32001, Message: "Session with given id not found."}}}
			}
			return []message{{ID: m.ID, SessionID: m.SessionID, Result: json.RawMessage(`{"v":"` + big + `"}`)}}
		}
		return []message{{ID: m.ID, Error: &Error{Code: -32601, Message: "'" + m.Method + "' wasn't found"}}}
	})
	events := make(chan Event, 4)
	cancel := c.Subscribe(func(e Event) { events <- e })
	defer cancel()
	ctx := context.Background()
	v, err := c.Call(ctx, "", "Browser.getVersion", nil)
	if err != nil || string(v) != `{"product":"Chrome/1"}` {
		t.Fatalf("%s %v", v, err)
	}
	select {
	case e := <-events:
		if e.Method != "Target.targetCreated" || string(e.Params) != `{"a":1}` {
			t.Errorf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Error("no event")
	}
	v, err = c.Call(ctx, "S1", "Runtime.evaluate", map[string]any{"expression": "1"})
	if err != nil || len(v) != len(big)+8 {
		t.Fatalf("big reply %d %v", len(v), err)
	}
	_, err = c.Call(ctx, "S2", "Runtime.evaluate", nil)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != -32001 {
		t.Errorf("session error %v", err)
	}
	if _, err = c.Call(ctx, "", "Nope.nope", nil); err == nil || !strings.Contains(err.Error(), "wasn't found") {
		t.Errorf("unknown method %v", err)
	}
}

func TestCancelAndClose(t *testing.T) {
	c, p := pipePair()
	hold := make(chan struct{})
	go p.serve(t, func(m message) []message {
		<-hold
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "", "Page.navigate", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout %v", err)
	}
	close(hold)
	// The browser going away fails calls in flight and later ones.
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "", "Page.reload", nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	p.out.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("in flight %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the call did not end")
	}
	<-c.Done()
	if _, err := c.Call(context.Background(), "", "Page.reload", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("after close %v", err)
	}
}
