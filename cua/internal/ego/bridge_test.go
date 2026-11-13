package ego

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, f *fakeEgo) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "fb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c := &Client{Dir: dir, Start: f.starter(), Timeout: time.Second}
	t.Cleanup(c.Close)
	return c
}

// One request, one reply, matched by id, also when they overlap; the
// bridge starts once.
func TestBridgeFraming(t *testing.T) {
	f := newFake()
	f.delay["slow"] = 200 * time.Millisecond
	c := testClient(t, f)
	ctx := context.Background()
	v, gen, err := c.Do(ctx, "space", map[string]any{"name": "s"})
	if err != nil || gen != 1 {
		t.Fatalf("%v %d", err, gen)
	}
	var sp struct {
		SpaceID int `json:"spaceId"`
	}
	_ = json.Unmarshal(v, &sp)
	slow := make(chan error, 1)
	go func() {
		_, _, err := c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "method": "slow"})
		slow <- err
	}()
	time.Sleep(20 * time.Millisecond)
	v, _, err = c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "method": "tabs"})
	if err != nil || !strings.Contains(string(v), `"label":"p1"`) {
		t.Fatalf("tabs %s %v", v, err)
	}
	var re *RemoteError
	if err := <-slow; !errors.As(err, &re) || !strings.Contains(re.Message, "no task method slow") {
		t.Errorf("slow: %v", err)
	}
	if f.starts != 1 {
		t.Errorf("%d bridges started", f.starts)
	}
}

// A dial without the token is refused; the right one is served.
func TestBridgeToken(t *testing.T) {
	f := newFake()
	f.badToken = true
	c := testClient(t, f)
	if _, _, err := c.Do(context.Background(), "spaces", nil); err != nil {
		t.Fatal(err)
	}
}

// A request past its timeout fails as a deadline; the bridge stays.
func TestBridgeTimeout(t *testing.T) {
	f := newFake()
	f.delay["tabs"] = 1500 * time.Millisecond
	c := testClient(t, f)
	c.Timeout = 100 * time.Millisecond
	v, _, _ := c.Do(context.Background(), "space", map[string]any{"name": "s"})
	var sp struct {
		SpaceID int `json:"spaceId"`
	}
	_ = json.Unmarshal(v, &sp)
	_, _, err := c.Do(context.Background(), "call", map[string]any{"spaceId": sp.SpaceID, "method": "tabs"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if !c.Live() || f.starts != 1 {
		t.Error("the bridge did not survive a timeout")
	}
}

// A cancelled context (Stop) ends the wait at once.
func TestBridgeCancel(t *testing.T) {
	f := newFake()
	f.delay["spaces"] = time.Second
	c := testClient(t, f)
	c.Timeout = 5 * time.Second
	if _, _, err := c.Do(context.Background(), "space", map[string]any{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _, err := c.Do(ctx, "spaces", nil)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

// A bridge that dies fails what was in flight; the next request starts a
// new one under a new generation.
func TestBridgeCrashRestart(t *testing.T) {
	f := newFake()
	f.delay["spaces"] = 300 * time.Millisecond
	c := testClient(t, f)
	if _, gen, err := c.Do(context.Background(), "space", map[string]any{"name": "x"}); err != nil || gen != 1 {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := c.Do(context.Background(), "spaces", nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	f.crash()
	if err := <-done; !errors.Is(err, ErrBridgeGone) {
		t.Fatalf("in flight: %v", err)
	}
	f.delay["spaces"] = 0
	_, gen, err := c.Do(context.Background(), "spaces", nil)
	if err != nil || gen != 2 || f.starts != 2 {
		t.Fatalf("restart: %v gen %d starts %d", err, gen, f.starts)
	}
}

// Close says goodbye; later requests fail without starting a bridge.
func TestBridgeClose(t *testing.T) {
	f := newFake()
	c := testClient(t, f)
	if _, _, err := c.Do(context.Background(), "spaces", nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if c.Live() {
		t.Error("live after close")
	}
	if _, _, err := c.Do(context.Background(), "spaces", nil); err == nil || f.starts != 1 {
		t.Errorf("after close: %v, %d starts", err, f.starts)
	}
}

func TestBridgeStartFailures(t *testing.T) {
	c := &Client{Dir: "/tmp/" + strings.Repeat("x", 120)}
	c.Start = newFake().starter()
	if _, _, err := c.Do(context.Background(), "spaces", nil); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long path: %v", err)
	}
	dir, _ := os.MkdirTemp("", "fb")
	defer os.RemoveAll(dir)
	exited := &Client{Dir: dir, Start: func(ctx context.Context, sock, token, script string) (Proc, error) {
		p := &fakeProc{done: make(chan struct{})}
		p.Kill()
		return p, nil
	}}
	if _, _, err := exited.Do(context.Background(), "spaces", nil); err == nil || !strings.Contains(err.Error(), "exited before it connected") {
		t.Errorf("exit: %v", err)
	}
	none := &Client{Dir: dir}
	if _, _, err := none.Do(context.Background(), "spaces", nil); err == nil {
		t.Error("no starter answered")
	}
}

// The embedded script is the bridge: it defines nothing itself but serves
// the documented ops through the SDK's globals.
func TestBridgeScript(t *testing.T) {
	for _, want := range []string{"taskSpace(a.name)", "takeOverTaskSpace(a.spaceId)", "listTaskSpaces()", "task.adopt(tab.page)", `JSON.stringify({ hello: TOKEN })`, `req.op === "bye"`, `sock.on("close", () => process.exit(0))`} {
		if !strings.Contains(bridgeJS, want) {
			t.Errorf("the bridge lacks %s", want)
		}
	}
	for _, banned := range []string{"claimTaskSpace", "child_process", "process.env"} {
		if strings.Contains(bridgeJS, banned) {
			t.Errorf("the bridge uses %s", banned)
		}
	}
}
