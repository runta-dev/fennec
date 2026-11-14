package terminalstream

import (
	"bytes"
	"context"
	"fennec/internal/clipboardbridge"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This opt-in acceptance test opens a real agent composer without submitting a
// prompt or making an inference request. It owns and closes only its own PTY.
func TestLiveCodexImagePaste(t *testing.T) {
	liveAgentImagePaste(t, "codex")
}

func TestLiveKimiImagePaste(t *testing.T) {
	liveAgentImagePaste(t, "kimi")
}

func liveAgentImagePaste(t *testing.T, agent string) {
	if os.Getenv("FENNEC_LIVE_IMAGE_PASTE") != "1" {
		t.Skip("explicit live image paste test")
	}
	directory := t.TempDir()
	id := fmt.Sprintf("%032x", time.Now().UnixNano())
	var bin string
	if agent == "kimi" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		binary := os.Getenv("FENNEC_LIVE_CLIPBOARD_ADAPTER")
		if err := clipboardbridge.Prepare(home, id, binary); err != nil {
			t.Fatal(err)
		}
		bin, _ = clipboardbridge.Bin(home, id)
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(bin)) })
	}
	t.Setenv("TMPDIR", directory)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, input, output, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			command := exec.Command("codex", "--no-daemon", "--no-alt-screen", "-c", fmt.Sprintf("projects.%q.trust_level=\"trusted\"", directory))
			command.Dir = directory
			command.Env = append(os.Environ(), "TERM=xterm-256color")
			terminal := cloudruntime.LocalTerminal{ID: request.ID, Agent: request.Agent, Address: "t1", State: "live"}
			if agent == "kimi" {
				home, _ := os.UserHomeDir()
				command = exec.Command("kimi", "--auto")
				command.Dir = directory
				command.Env = append(os.Environ(), "TERM=xterm-256color", "KIMI_CODE_TRUST_WORKSPACE=1", "KIMI_CODE_HOME="+filepath.Join(home, ".local/fennec/native/kimi"), clipboardbridge.SessionEnv+"="+id, "XDG_SESSION_TYPE=wayland", "PATH="+bin+":"+os.Getenv("PATH"))
				terminal.Labels = map[string]string{clipboardbridge.Label: clipboardbridge.Version}
			}
			return terminal, command, nil
		})
	}()
	transport := StartPipes(send, receive)
	defer func() {
		cancel()
		transport.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("test PTY cleanup timed out")
		}
	}()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	request := cloudruntime.LocalLaunch{ID: id, Agent: agent, LocalDirectory: directory, Device: "1234abcd"}
	session, err := transport.Launch(Frame{Type: "launch", ID: request.ID, Launch: &request, Rows: 40, Columns: 120, Display: &DisplayFacts{Foreground: "#cccccc", Background: "#181818", Cursor: "#cccccc", CellWidth: 8, CellHeight: 16}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	window := NewCreditWindow()
	defer window.Close()
	stop := context.AfterFunc(ctx, window.Close)
	defer stop()
	var shown bytes.Buffer
	ansi := regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	sent := false
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("composer did not confirm attachment: %v\n%s", err, ansi.ReplaceAll(shown.Bytes(), nil))
		}
		switch frame.Type {
		case "data":
			shown.Write(frame.Data)
			if shown.Len() > 1<<20 {
				t.Fatal("unexpected terminal output volume")
			}
			session.Send(Frame{Type: "credit", Bytes: len(frame.Data)})
			plain := string(ansi.ReplaceAll(shown.Bytes(), nil))
			ready := strings.Contains(plain, "for agents") && strings.Contains(plain, "state=idle:app=codex")
			if agent == "kimi" {
				ready = strings.Contains(plain, "Welcome to Kimi Code!") && strings.Contains(plain, "Never Ask")
			}
			if !sent && ready {
				sent = true
				data := testImage(t)
				go func() {
					total := len(data)
					for len(data) > 0 {
						n := window.Take(min(Chunk, len(data)))
						if n == 0 {
							return
						}
						if session.Send(Frame{Type: "image", Data: data[:n], Bytes: total}) != nil {
							return
						}
						data, total = data[n:], 0
					}
				}()
			}
			marker := "[Image #1]"
			if agent == "kimi" {
				marker = "[image #1 (256×256)]"
			}
			if sent && strings.Contains(plain, marker) {
				t.Logf("real %s composer accepted image attachment without an X11 clipboard or inference request", agent)
				return
			}
		case "input_credit":
			if !window.Add(frame.Bytes) {
				t.Fatal("invalid credit")
			}
		case "error", "paste_error", "exit":
			t.Fatalf("%+v\n%s", frame, ansi.ReplaceAll(shown.Bytes(), nil))
		}
	}
}
