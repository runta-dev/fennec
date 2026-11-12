package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cuab")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type bridgeHarness struct {
	*harness
	done chan error
}

func runBridge(t *testing.T, cfg BridgeConfig) *bridgeHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := Bridge(context.Background(), inR, outW, cfg)
		_ = outW.Close()
		done <- err
	}()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	t.Cleanup(func() { _ = inW.Close() })
	return &bridgeHarness{harness: &harness{t: t, in: inW, out: sc}, done: done}
}

// rawDaemon accepts connections and hands each request line to serve.
type rawDaemon struct {
	mu    sync.Mutex
	lines []string
	conns atomic.Int32
}

func (d *rawDaemon) listen(t *testing.T, sock string, serve func(n int32, conn net.Conn, line string) bool) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := d.conns.Add(1)
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					d.mu.Lock()
					d.lines = append(d.lines, strings.TrimSpace(line))
					d.mu.Unlock()
					if !serve(n, c, line) {
						return
					}
				}
			}()
		}
	}()
	return ln
}

func (d *rawDaemon) count(substr string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, l := range d.lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func TestNoResend(t *testing.T) {
	sock := filepath.Join(shortDir(t), "d.sock")
	d := &rawDaemon{}
	d.listen(t, sock, func(n int32, c net.Conn, line string) bool {
		var m message
		_ = json.Unmarshal([]byte(line), &m)
		if strings.Contains(line, `"type_text"`) {
			return false // the call landed, then the daemon died
		}
		if len(m.ID) > 0 {
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{}}` + "\n"))
		}
		return true
	})
	b := runBridge(t, BridgeConfig{Socket: sock, Start: func() (<-chan error, error) { t.Error("started a daemon while one listened"); return nil, nil }})
	b.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if m := b.recv(); m["id"] != 1.0 || m["error"] != nil {
		t.Fatalf("initialize %v", m)
	}
	b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act","arguments":{"type_text":"hello"}}}`)
	m := b.recvWithin(3 * time.Second)
	if m["id"] != 2.0 || !strings.Contains(errMessage(m), "not sent again") {
		t.Fatalf("dropped call answered %v", m)
	}
	// A later request reconnects; the dropped call is never resent.
	b.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if m := b.recvWithin(3 * time.Second); m["id"] != 3.0 || m["error"] != nil {
		t.Fatalf("after the drop: %v", m)
	}
	time.Sleep(50 * time.Millisecond)
	if n := d.count(`"type_text"`); n != 1 {
		t.Errorf("the call was sent %d times", n)
	}
	if d.conns.Load() != 2 {
		t.Errorf("connections %d", d.conns.Load())
	}
	// The client's handshake was replayed on the new connection under the
	// bridge's own id, whose answer the client never sees.
	if n := d.count(`"initialize"`); n != 2 {
		t.Errorf("initialize sent %d times", n)
	}
}

func TestBridgeConnectErrors(t *testing.T) {
	// Permission denied: reported, no second daemon.
	var started atomic.Int32
	start := func() (<-chan error, error) { started.Add(1); return nil, errors.New("no") }
	err := Bridge(context.Background(), strings.NewReader(""), io.Discard, BridgeConfig{
		Socket: "/x/cua.sock",
		Start:  start,
		Dial: func(string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EACCES)}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "not starting another daemon") || started.Load() != 0 {
		t.Errorf("EACCES: %v (started %d)", err, started.Load())
	}
	// A socket file a real permission check refuses.
	dir := shortDir(t)
	sock := filepath.Join(dir, "d.sock")
	d := &rawDaemon{}
	d.listen(t, sock, func(int32, net.Conn, string) bool { return true })
	_ = os.Chmod(sock, 0)
	if os.Getuid() != 0 {
		err = Bridge(context.Background(), strings.NewReader(""), io.Discard, BridgeConfig{Socket: sock, Start: start})
		if err == nil || started.Load() != 0 {
			t.Errorf("unreadable socket: %v (started %d)", err, started.Load())
		}
	}
	// Nothing listening (a stale file, or none) is the one case that may
	// start a daemon.
	stale := filepath.Join(dir, "stale.sock")
	ln, _ := net.Listen("unix", stale)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	for _, s := range []string{stale, filepath.Join(dir, "missing.sock")} {
		_, err := net.Dial("unix", s)
		if !NoDaemon(err) {
			t.Errorf("%s: %v is not 'no daemon'", s, err)
		}
	}
	if NoDaemon(os.NewSyscallError("connect", syscall.EACCES)) || NoDaemon(errors.New("other")) {
		t.Error("an unrelated error counts as no daemon")
	}
	b := runBridge(t, BridgeConfig{Socket: stale, Start: start, Poll: time.Millisecond})
	b.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe"}}`)
	if m := b.recv(); errCode(m) != CodeInternal || started.Load() != 1 {
		t.Errorf("stale socket: %v (started %d)", m, started.Load())
	}
}

func TestEmbeddedNoAutostart(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "cua.sock")
	var started atomic.Int32
	begin := time.Now()
	err := Bridge(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"), io.Discard, BridgeConfig{
		Socket:   sock,
		Embedded: true,
		Start:    func() (<-chan error, error) { started.Add(1); return nil, nil },
	})
	if err == nil || !errors.Is(err, ErrEmbeddedNoDaemon) || !strings.Contains(err.Error(), sock) || !strings.Contains(err.Error(), EmbeddedEnv) {
		t.Fatalf("err %v", err)
	}
	if time.Since(begin) > time.Second || started.Load() != 0 {
		t.Errorf("took %s, started %d", time.Since(begin), started.Load())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v behind", entries)
	}
	// With a daemon answering, embedded mode connects as usual.
	d := &rawDaemon{}
	d.listen(t, sock, func(_ int32, c net.Conn, line string) bool {
		_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"))
		return true
	})
	b := runBridge(t, BridgeConfig{Socket: sock, Embedded: true})
	b.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if m := b.recv(); m["id"] != 1.0 {
		t.Errorf("embedded with a daemon: %v", m)
	}
}
