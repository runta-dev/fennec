package fixture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEnv makes the test binary act as a fake CUAFixture: Launch tests copy
// it into a bundle layout and start it.
const fakeEnv = "CUA_FIXTURE_FAKE_APP"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		runFakeApp(os.Getenv("CUA_FIXTURE_SOCKET"))
		return
	}
	os.Exit(m.Run())
}

// fake is an in-process stand-in for the app's control socket.
type fake struct {
	path string
	ln   net.Listener

	mu       sync.Mutex
	scene    string
	requests []map[string]any
	hang     bool
	quit     chan struct{}
}

func socketDir(t *testing.T) string {
	t.Helper()
	// t.TempDir paths can exceed the 104-byte unix socket limit on macOS.
	dir, err := os.MkdirTemp("", "cfx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startFake(t *testing.T, path string) *fake {
	if path == "" {
		path = filepath.Join(socketDir(t), "s")
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatal(err)
	}
	f := &fake{path: path, ln: ln, scene: "basic", quit: make(chan struct{})}
	go f.serve()
	if t != nil {
		t.Cleanup(func() { ln.Close() })
	}
	return f
}

func (f *fake) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.conn(c)
	}
}

func (f *fake) conn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req map[string]any
		var ans map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			ans = map[string]any{"ok": false, "error": "request is not a JSON object"}
		} else {
			f.mu.Lock()
			f.requests = append(f.requests, req)
			hang := f.hang
			f.mu.Unlock()
			if hang {
				time.Sleep(5 * time.Second)
				return
			}
			ans = f.answer(req)
		}
		data, _ := json.Marshal(ans)
		c.Write(append(data, '\n'))
		if req["op"] == "quit" {
			close(f.quit)
			return
		}
	}
}

var fakeScenes = []string{"basic", "form", "empty"}

func (f *fake) answer(req map[string]any) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req["op"] {
	case "scenes":
		return map[string]any{"ok": true, "scenes": fakeScenes}
	case "reset":
		s, _ := req["scene"].(string)
		for _, n := range fakeScenes {
			if n == s {
				f.scene = s
				return map[string]any{"ok": true}
			}
		}
		return map[string]any{"ok": false, "error": "unknown scene " + s}
	case "state":
		if f.scene == "busy" {
			return map[string]any{"ok": false, "error": "app is busy: main thread did not answer", "busy": true}
		}
		return map[string]any{"ok": true, "state": map[string]any{
			"scene":    f.scene,
			"presses":  map[string]any{"increment": 2},
			"volatile": map[string]any{"pid": 42},
		}}
	case "freeze", "quit":
		return map[string]any{"ok": true}
	}
	return map[string]any{"ok": false, "error": "unknown op"}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestResetStateScenes(t *testing.T) {
	f := startFake(t, "")
	c := New(f.path)
	if err := c.Reset(ctx(t), "form"); err != nil {
		t.Fatal(err)
	}
	st, err := c.State(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"scene": "form", "presses": map[string]any{"increment": float64(2)}}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("state = %v, want %v", st, want)
	}
	raw, err := c.RawState(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := raw[VolatileKey]; !ok {
		t.Fatal("RawState dropped the volatile part")
	}
	names, err := c.Scenes(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, fakeScenes) {
		t.Fatalf("scenes = %v", names)
	}
}

func TestRequestsCarryOpAndTimeout(t *testing.T) {
	f := startFake(t, "")
	c := New(f.path)
	if err := c.Reset(ctx(t), "basic"); err != nil {
		t.Fatal(err)
	}
	if err := c.Freeze(context.Background(), 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 {
		t.Fatalf("requests = %v", f.requests)
	}
	r := f.requests[0]
	if r["op"] != "reset" || r["scene"] != "basic" {
		t.Fatalf("reset request = %v", r)
	}
	if ms, ok := r["timeout_ms"].(float64); !ok || ms <= 0 || ms > 5000 {
		t.Fatalf("reset request timeout_ms = %v; want the context's remaining time", r["timeout_ms"])
	}
	fr := f.requests[1]
	if fr["op"] != "freeze" || fr["seconds"] != 1.5 {
		t.Fatalf("freeze request = %v", fr)
	}
	if _, ok := fr["timeout_ms"]; ok {
		t.Fatal("a request without a deadline sent timeout_ms")
	}
}

func TestErrorAnswers(t *testing.T) {
	f := startFake(t, "")
	c := New(f.path)
	err := c.Reset(ctx(t), "nope")
	var fe *Error
	if !errors.As(err, &fe) || fe.Op != "reset" || !strings.Contains(fe.Message, "unknown scene") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Call(ctx(t), map[string]any{"op": "bogus"}); !errors.As(err, &fe) || fe.Message != "unknown op" {
		t.Fatalf("unknown op err = %v", err)
	}
	f.mu.Lock()
	f.scene = "busy"
	f.mu.Unlock()
	_, err = c.State(ctx(t))
	if !errors.As(err, &fe) || !fe.Busy {
		t.Fatalf("busy err = %v", err)
	}
}

func TestContextEndsAHungCall(t *testing.T) {
	f := startFake(t, "")
	f.mu.Lock()
	f.hang = true
	f.mu.Unlock()
	c := New(f.path)
	cctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.State(cctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("call took %v after its deadline", d)
	}
}

func TestNoServer(t *testing.T) {
	c := New(filepath.Join(socketDir(t), "missing"))
	if _, err := c.Scenes(ctx(t)); err == nil {
		t.Fatal("call to a missing socket succeeded")
	}
}

func TestLaunchRefusesBadInput(t *testing.T) {
	if _, err := Launch(ctx(t), "/nonexistent/CUAFixture.app", filepath.Join(socketDir(t), "s")); err == nil {
		t.Fatal("launch of a missing bundle succeeded")
	}
	long := "/tmp/" + strings.Repeat("x", 120)
	if _, err := Launch(ctx(t), "/nonexistent/CUAFixture.app", long); err == nil || !strings.Contains(err.Error(), "103") {
		t.Fatalf("long socket path err = %v", err)
	}
}

// TestLaunchAndClose starts this test binary as a fake app inside a bundle
// layout, waits for its socket, uses it and quits it.
func TestLaunchAndClose(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := socketDir(t)
	app := filepath.Join(dir, "CUAFixture.app")
	if err := os.MkdirAll(filepath.Dir(Executable(app)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, Executable(app)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEnv, "1")
	sock := filepath.Join(dir, "s")
	a, err := Launch(ctx(t), app, sock)
	if err != nil {
		t.Fatal(err)
	}
	if a.Pid <= 0 {
		t.Fatalf("pid = %d", a.Pid)
	}
	if err := a.Reset(ctx(t), "empty"); err != nil {
		t.Fatal(err)
	}
	st, err := a.State(ctx(t))
	if err != nil || st["scene"] != "empty" {
		t.Fatalf("state = %v, %v", st, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.done:
	default:
		t.Fatal("app still running after Close")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}

// runFakeApp serves the fake protocol on path until asked to quit.
func runFakeApp(path string) {
	f := startFake(nil, path)
	select {
	case <-f.quit:
	case <-time.After(30 * time.Second):
	}
	f.ln.Close()
	os.Remove(path)
	os.Exit(0)
}
