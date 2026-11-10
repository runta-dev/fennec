package launcher

import (
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestStreamExitChild(t *testing.T) {
	home := os.Getenv("FENNEC_STREAM_EXIT_HOME")
	if home == "" {
		return
	}
	id := strings.Repeat("a", 32)
	plan := LaunchPlan{StreamPath: filepath.Join(home, ".config/fennec/ipc", "terminal-"+id), RuntimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", SessionID: id, Agent: "codex", Resume: true}
	if err := ExecuteStream(plan, LaunchIntent{RequestID: id, Agent: "codex"}, home, func() {}); err != nil {
		fmt.Fprintln(os.Stderr, "STREAM_FAILED", err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestExitFrameSurvivesFinalCreditWriteFailure(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-stream-exit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	directory := filepath.Join(home, ".config/fennec/ipc")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(directory, "terminal-"+id), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		var hello terminalstream.Frame
		if json.NewDecoder(conn).Decode(&hello) != nil {
			return
		}
		conn.CloseRead()
		encoder := json.NewEncoder(conn)
		encoder.Encode(terminalstream.Frame{Type: "receipt", ID: id, Terminal: &cloudruntime.LocalTerminal{ID: id, Agent: "codex", Address: "t1", State: "live"}})
		encoder.Encode(terminalstream.Frame{Type: "data", ID: id, Data: []byte("TAIL")})
		encoder.Encode(terminalstream.Frame{Type: "exit", ID: id, Code: 0})
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestStreamExitChild$")
	command.Env = append(os.Environ(), "FENNEC_STREAM_EXIT_HOME="+home, "TERM=xterm-256color")
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	output, _ := io.ReadAll(terminal)
	if err := command.Wait(); err != nil || !strings.Contains(string(output), "TAIL") || strings.Contains(string(output), "STREAM_FAILED") {
		t.Fatalf("normal exit treated as disconnect: %v %q", err, output)
	}
}
