package cmuxhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fn-cmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func TestSocketRoundTripAndDisconnectedNoOp(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeListener, err := Listen(ctx, home, "petite-neat", func(_ context.Context, event string, payload []byte) Response {
		if event != "inbox-stop" || !bytes.Contains(payload, []byte("fixture")) {
			t.Error("wrong hook delivered")
		}
		return Response{Code: 2, Data: json.RawMessage(`{"decision":"block","reason":"fixture"}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	result := Invoke(ctx, home, "petite-neat", "inbox-stop", bytes.NewBufferString(`{"session_id":"fixture"}`))
	if result.Code != 2 {
		t.Fatal(result)
	}
	if _, err := Listen(ctx, home, "petite-neat", nil); !errors.Is(err, ErrBusy) {
		t.Fatal("active hook endpoint stolen", err)
	}
	closeListener()
	result = Invoke(ctx, home, "petite-neat", "stop", bytes.NewBufferString(`{}`))
	if result.Code != 0 || string(result.Data) != "{}" {
		t.Fatal("desktop disconnect became hook failure", result)
	}
}
func TestConcurrentHookSessionsStayIsolated(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []string{"petite-neat", "gentle-fox"} {
		id := id
		closeListener, err := Listen(ctx, home, id, func(context.Context, string, []byte) Response {
			b, _ := json.Marshal(map[string]string{"session": id})
			return Response{Data: b}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer closeListener()
	}
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			id := []string{"petite-neat", "gentle-fox"}[i%2]
			result := Invoke(ctx, home, id, "stop", bytes.NewBufferString(`{}`))
			if !bytes.Contains(result.Data, []byte(id)) {
				t.Error("cross-session response")
			}
		}(i)
	}
	workers.Wait()
	path, _ := SocketPath(home, "petite-neat")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket not private", err)
	}
}
func TestHookSocketPreservesUnexpectedFiles(t *testing.T) {
	home := shortHome(t)
	path, _ := SocketPath(home, "petite-neat")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("user file"), 0600)
	if _, err := Listen(context.Background(), home, "petite-neat", nil); err == nil {
		t.Fatal("unexpected file replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "user file" {
		t.Fatal("unexpected file lost")
	}
}
