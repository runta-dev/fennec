package launcher

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMarkAttachedLabelsOwnTerminalAndClears(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fn-label-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	values := make(chan string, 4)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(connection).ReadBytes('\n')
			var request struct {
				Method string
				Params struct {
					Terminal string
					Labels   map[string]string
				}
			}
			if json.Unmarshal(line, &request) == nil && request.Method == "terminal.label" && request.Params.Terminal == "t7" {
				values <- request.Params.Labels[AttachedLabel]
			}
			connection.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"))
			connection.Close()
		}
	}()
	t.Setenv("HC_TERMINAL_ID", "t7")
	t.Setenv("HC_TERMINAL_SOCKET", socket)
	id := "0123456789abcdef0123456789abcdef"
	clear := markAttached(id)
	clear()
	clear()
	for _, want := range []string{id, ""} {
		select {
		case got := <-values:
			if got != want {
				t.Fatalf("label = %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no label call for %q", want)
		}
	}
	select {
	case got := <-values:
		t.Fatalf("unexpected extra label call %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMarkAttachedOutsideHerdrTerminalDoesNothing(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "")
	t.Setenv("HC_TERMINAL_SOCKET", "")
	markAttached("0123456789abcdef0123456789abcdef")()
}
