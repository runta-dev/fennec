package ope2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Run inside scripts/tests/clipboard-helper.swift, which owns a known image
// fixture and restores the Mac clipboard after the child exits. No prompt is
// submitted: acceptance is the native agent's attachment marker in its draft.
func TestLiveImagePasteClient(t *testing.T) {
	binary := os.Getenv("FENNEC_LIVE_IMAGE_CLIENT_BINARY")
	if binary == "" {
		t.Skip("explicit installed-client image paste test")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("installed client path must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := liveClient{binary: binary}
	agent := os.Getenv("FENNEC_LIVE_IMAGE_AGENT")
	if agent == "" {
		agent = "codex"
	}
	if agent != "codex" && agent != "kimi" {
		t.Fatal("unsupported test agent")
	}
	arguments := []string{"--no-alt-screen"}
	if agent == "kimi" {
		arguments = nil
	}
	raw, _, err := client.call(ctx, "sessions.allocate", map[string]string{"agent": agent})
	if err != nil {
		t.Fatal(err)
	}
	var allocation struct {
		ID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &allocation) != nil || allocation.ID == "" {
		t.Fatal("invalid allocation")
	}
	raw, _, err = client.call(ctx, "sessions.launch", map[string]any{"session_id": allocation.ID, "agent": agent, "directory": t.TempDir(), "arguments": arguments})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if _, _, err := client.call(cleanup, "sessions.stop", map[string]string{"session_id": allocation.ID}); err != nil {
			t.Errorf("owned test session cleanup: %v", err)
		}
	}()
	command := exec.CommandContext(ctx, binary, "fen", "attach", allocation.ID)
	command.Env = append(os.Environ(), "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	var output bytes.Buffer
	ansi := regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	queries := map[string]string{"\x1b]10;?": "\x1b]10;rgb:cccc/cccc/cccc\x1b\\", "\x1b]11;?": "\x1b]11;rgb:1818/1818/1818\x1b\\", "\x1b]12;?": "\x1b]12;rgb:cccc/cccc/cccc\x1b\\", "\x1b[16t": "\x1b[6;16;8t", "\x1b[c": "\x1b[?1;2c", "\x1b[6n": "\x1b[1;1R"}
	buffer := make([]byte, 16384)
	var pasted time.Time
	accepted := 0
	for {
		n, readErr := terminal.Read(buffer)
		if n > 0 {
			output.Write(buffer[:n])
			if output.Len() > 1<<20 {
				cancel()
				break
			}
			for query, reply := range queries {
				if bytes.Contains(buffer[:n], []byte(query)) {
					terminal.Write([]byte(reply))
				}
			}
			plain := string(ansi.ReplaceAll(output.Bytes(), nil))
			ready := strings.Contains(plain, "for agents")
			if agent == "kimi" {
				ready = strings.Contains(plain, "Welcome to Kimi Code!") && strings.Contains(plain, "Never Ask")
			}
			if pasted.IsZero() && ready {
				pasted = time.Now()
				terminal.Write([]byte{22})
			}
			marker := fmt.Sprintf("[Image #%d]", accepted+1)
			if agent == "kimi" {
				marker = fmt.Sprintf("[image #%d (", accepted+1)
			}
			if !pasted.IsZero() && accepted < 3 && strings.Contains(plain, marker) {
				accepted++
				t.Logf("Mac clipboard -> installed client -> resident channel -> %s attachment %d: %s", agent, accepted, time.Since(pasted))
				if accepted == 3 {
					terminal.Write([]byte{28})
				} else {
					pasted = time.Now()
					terminal.Write([]byte{22})
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if err := command.Wait(); err != nil || accepted != 3 {
		t.Fatalf("image paste not confirmed: %v\n%s", err, ansi.ReplaceAll(output.Bytes(), nil))
	}
}
