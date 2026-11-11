package hookbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fn-hooks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func fixtureBinding(policy Policy) Binding {
	return Binding{ID: strings.Repeat("a", 64), Name: "Fixture", Event: "PreToolUse", Policy: policy, TimeoutMS: 1000}
}
func TestSocketPreservesRawOutputAndPolicyOnDisconnect(t *testing.T) {
	for _, policy := range []Policy{Observer, Required} {
		t.Run(string(policy), func(t *testing.T) {
			home := shortHome(t)
			binding := fixtureBinding(policy)
			if err := SaveBindings(home, "petite-neat", []Binding{binding}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			closeListener, err := Listen(ctx, home, "petite-neat", func(_ context.Context, b Binding, input []byte) Result {
				if b.Policy != policy || string(input) != "fixture input" {
					t.Error("binding changed")
				}
				return Result{Code: 23, Stdout: []byte("raw output\n"), Stderr: []byte("diagnostic\n"), Status: "executed"}
			})
			if err != nil {
				t.Fatal(err)
			}
			result := Invoke(ctx, home, "petite-neat", binding.ID, strings.NewReader("fixture input"))
			if result.Code != 23 || string(result.Stdout) != "raw output\n" || string(result.Stderr) != "diagnostic\n" {
				t.Fatal(result)
			}
			if _, err := Listen(ctx, home, "petite-neat", nil); !errors.Is(err, ErrBusy) {
				t.Fatal("endpoint stolen", err)
			}
			closeListener()
			result = Invoke(ctx, home, "petite-neat", binding.ID, strings.NewReader("{}"))
			if policy == Required && (result.Code != 2 || result.Status != "blocked") {
				t.Fatal("required hook failed open", result)
			}
			if policy == Observer && (result.Code != 0 || result.Status != "skipped") {
				t.Fatal("observer did not skip", result)
			}
		})
	}
}
func TestConcurrentHookSessionsStayIsolated(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	binding := fixtureBinding(Required)
	for _, id := range []string{"petite-neat", "gentle-fox"} {
		id := id
		SaveBindings(home, id, []Binding{binding})
		closeListener, err := Listen(ctx, home, id, func(context.Context, Binding, []byte) Result { return Result{Stdout: []byte(id), Status: "executed"} })
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
			result := Invoke(ctx, home, id, binding.ID, strings.NewReader("{}"))
			if string(result.Stdout) != id {
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
func TestGuestCannotDowngradePolicyOrChooseCommands(t *testing.T) {
	home := shortHome(t)
	binding := fixtureBinding(Required)
	SaveBindings(home, "petite-neat", []Binding{binding})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeListener, err := Listen(ctx, home, "petite-neat", func(context.Context, Binding, []byte) Result {
		t.Error("untrusted request reached executor")
		return Result{Status: "executed"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeListener()
	path, _ := SocketPath(home, "petite-neat")
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(time.Second))
	json.NewEncoder(connection).Encode(map[string]any{"binding_id": binding.ID, "input": []byte("{}"), "policy": "observer", "command": "/bin/sh"})
	var result Result
	if json.NewDecoder(connection).Decode(&result) != nil || result.Code != 2 {
		t.Fatal("request changed execution policy")
	}
	if result := Invoke(ctx, home, "petite-neat", strings.Repeat("b", 64), bytes.NewBufferString("{}")); result.Code != 2 {
		t.Fatal("unknown hook allowed")
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
