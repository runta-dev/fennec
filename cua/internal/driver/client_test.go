package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDriver is a line-JSON server; answer maps a request to a reply line.
type fakeDriver struct {
	ln       net.Listener
	conns    atomic.Int32
	mu       sync.Mutex
	requests []string
	answer   func(conn int32, name string, args json.RawMessage) string
}

func newFake(t *testing.T, answer func(conn int32, name string, args json.RawMessage) string) (*fakeDriver, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "drv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDriver{ln: ln, answer: answer}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := f.conns.Add(1)
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					var req struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					}
					_ = json.Unmarshal(line, &req)
					f.mu.Lock()
					f.requests = append(f.requests, req.Name)
					f.mu.Unlock()
					reply := f.answer(n, req.Name, req.Args)
					if reply == "" {
						return // hang up without answering
					}
					_, _ = c.Write([]byte(reply + "\n"))
				}
			}()
		}
	}()
	return f, sock
}

func TestCallReusesTheLaneConnection(t *testing.T) {
	f, sock := newFake(t, func(int32, string, json.RawMessage) string { return `{"ok":true,"result":{"w":1}}` })
	c := &Client{Socket: sock}
	defer c.Close()
	for i := 0; i < 3; i++ {
		res, err := c.Call(context.Background(), LaneInput, "get_screen_size", nil)
		if err != nil || string(res) != `{"w":1}` {
			t.Fatalf("%s %v", res, err)
		}
	}
	if _, err := c.Call(context.Background(), LaneCapture, "get_screen_size", nil); err != nil {
		t.Fatal(err)
	}
	if n := f.conns.Load(); n != 2 {
		t.Errorf("connections %d, want one per lane", n)
	}
}

func TestErrorsArePrefixedAndClassified(t *testing.T) {
	_, sock := newFake(t, func(_ int32, name string, _ json.RawMessage) string {
		switch name {
		case "empty":
			return `{"ok":true}`
		case "junk":
			return `not json`
		}
		return `{"ok":false,"error":"window not found","result":{"reason":"no_window"}}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	_, err := c.Call(context.Background(), LaneInput, "click", nil)
	var de *Error
	if !errors.As(err, &de) || err.Error() != "click: window not found" || de.Transport || !strings.Contains(string(de.Result), "no_window") {
		t.Errorf("tool error: %v", err)
	}
	if _, err := c.Call(context.Background(), LaneInput, "empty", nil); err == nil || !strings.Contains(err.Error(), "empty reply") {
		t.Errorf("empty: %v", err)
	}
	if _, err := c.Call(context.Background(), LaneInput, "junk", nil); err == nil || !strings.Contains(err.Error(), "undecodable") {
		t.Errorf("junk: %v", err)
	}
}

func TestEndedSessionIsRetriedOnceOnANewConnection(t *testing.T) {
	f, sock := newFake(t, func(conn int32, name string, _ json.RawMessage) string {
		if name == "mentions" {
			return `{"ok":false,"error":"the window's session has ended earlier"}`
		}
		if conn == 1 {
			return `{"ok":false,"error":"session has ended"}`
		}
		return `{"ok":true,"result":true}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	if _, err := c.Call(context.Background(), LaneInput, "click", nil); err != nil {
		t.Fatal(err)
	}
	if n := f.conns.Load(); n != 2 {
		t.Errorf("connections %d", n)
	}
	before := len(f.requests)
	if _, err := c.Call(context.Background(), LaneInput, "mentions", nil); err == nil {
		t.Error("a mere mention was retried into success")
	}
	if len(f.requests)-before != 1 {
		t.Errorf("mention sent %d times", len(f.requests)-before)
	}
}

func TestNoReplyIsNotResent(t *testing.T) {
	f, sock := newFake(t, func(int32, string, json.RawMessage) string { return "" })
	c := &Client{Socket: sock}
	defer c.Close()
	_, err := c.Call(context.Background(), LaneInput, "type_text", nil)
	if err == nil || !strings.Contains(err.Error(), "may have been sent") {
		t.Fatalf("err %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Errorf("sent %d times", len(f.requests))
	}
}

func TestConnectWaitsForARestartingDriver(t *testing.T) {
	dir, _ := os.MkdirTemp("", "drv")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "late.sock")
	go func() {
		time.Sleep(600 * time.Millisecond)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_, _ = conn.Write([]byte(`{"ok":true,"result":1}` + "\n"))
	}()
	c := &Client{Socket: sock}
	defer c.Close()
	start := time.Now()
	if _, err := c.Call(context.Background(), LaneInput, "x", nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("connected before the driver listened")
	}
}

func TestCancelInterruptsAWaitingCall(t *testing.T) {
	block := make(chan struct{})
	_, sock := newFake(t, func(int32, string, json.RawMessage) string { <-block; return `{"ok":true,"result":1}` })
	defer close(block)
	c := &Client{Socket: sock}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, LaneInput, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
}

func TestTypeTimeoutCountsUTF16Units(t *testing.T) {
	if got := TypeTimeout("a😀"); got != RequestTimeout+3*PerTypedUnit {
		t.Errorf("got %s", got)
	}
}
