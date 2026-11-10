package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/launcher"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestFenSessionListFiltersAndKeepsIdentity(t *testing.T) {
	id := strings.Repeat("a", 32)
	data, _ := json.Marshal(map[string]any{"terminals": []map[string]any{
		{"address": "t1", "state": "live", "title": "查看仓库 | 6629244c4e1d9427135cf...", "status": map[string]string{"state": "blocked"}, "labels": map[string]string{"fennec.session": id, "fennec.agent": "codex"}},
		{"address": "t2", "state": "finished", "agent": "codex", "labels": map[string]string{"fennec.session": strings.Repeat("b", 32)}},
		{"address": "t3", "state": "live", "agent": "sh"},
	}})
	sessions, err := fenParseSessions(data, "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e")
	if err != nil || len(sessions) != 1 || sessions[0].Title != "查看仓库" || sessions[0].Status != "Needs input" || sessions[0].Receipt.SessionID != id {
		t.Fatal(sessions, err)
	}
	if strings.ContainsRune(fenTitle("\x1b[31mTitle\n"), '\x1b') {
		t.Fatal("terminal escape retained")
	}
}

func TestFenCommandRejectsInvalidArgumentsBeforeIO(t *testing.T) {
	for _, arguments := range [][]string{{"kill"}, {"resume", "../bad"}, {"resume", strings.Repeat("a", 32), "extra"}} {
		if err := fenCommand(arguments, io.Discard); err == nil {
			t.Fatal(arguments)
		}
	}
	var output strings.Builder
	if err := fenCommand([]string{"--help"}, &output); err != nil || !strings.Contains(output.String(), "x kill") || !strings.Contains(output.String(), "j/k") {
		t.Fatal(output.String(), err)
	}
}

func TestFenPickerFrameKeepsFooterAndHidesIDs(t *testing.T) {
	sessions := []fenSession{{Receipt: launcher.LaunchReceipt{Agent: "codex", SessionID: strings.Repeat("a", 32)}, Title: "修复登录问题", Status: "Idle"}}
	for _, width := range []int{28, 80} {
		frame := fenPickerFrame(sessions, 0, width, 12, false, "")
		for _, expected := range []string{"› 1.", "x kill", "esc quit", "enter attach"} {
			if !strings.Contains(frame, expected) {
				t.Fatal(width, expected, frame)
			}
		}
		if strings.Contains(frame, sessions[0].Receipt.SessionID) {
			t.Fatal("session ID leaked into picker")
		}
	}
}

func TestFenPickerKeyboardAndTerminalRestore(t *testing.T) {
	if os.Getenv("FENNEC_PICKER_FIXTURE") == "1" {
		sessions := []fenSession{{Receipt: launcher.LaunchReceipt{Agent: "codex", SessionID: "alpha"}, Title: "Alpha", Status: "Idle"}, {Receipt: launcher.LaunchReceipt{Agent: "claude", SessionID: "beta"}, Title: "Beta", Status: "Working"}}
		kills := 0
		selected, err := fenPick(context.Background(), sessions, func() ([]fenSession, error) { return sessions, nil }, func(session fenSession) error {
			if session.Receipt.SessionID != "alpha" {
				panic("wrong kill target")
			}
			kills++
			return nil
		})
		if err != nil || selected == nil {
			os.Exit(2)
		}
		fmt.Printf("RESULT %s KILLS %d\n", selected.Receipt.SessionID, kills)
		os.Exit(0)
	}
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprint(confirm), func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80})
			before, err := fenTerminalState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestFenPickerKeyboardAndTerminalRestore$")
			command.Env = append(os.Environ(), "FENNEC_PICKER_FIXTURE=1", "TERM=xterm-256color")
			command.Stdin = slave
			command.Stdout = slave
			command.Stderr = slave
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			chunks := make(chan string, 64)
			go func() {
				defer close(chunks)
				var data [8192]byte
				for {
					count, err := master.Read(data[:])
					if count > 0 {
						chunks <- string(data[:count])
					}
					if err != nil {
						return
					}
				}
			}()
			readUntil := func(expected string) {
				t.Helper()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				output := ""
				for {
					select {
					case chunk, ok := <-chunks:
						if !ok {
							t.Fatalf("PTY closed before %s: %s", expected, output)
						}
						output += chunk
						if strings.Contains(output, expected) {
							return
						}
					case <-deadline.C:
						t.Fatalf("missing %s: %s", expected, output)
					}
				}
			}
			readUntil("esc quit")
			master.Write([]byte("jkx"))
			readUntil("Kill “Alpha”?")
			if confirm {
				master.Write([]byte("\r"))
				readUntil("Session stopped.")
				master.Write([]byte("\r"))
				readUntil("RESULT beta KILLS 1")
			} else {
				master.Write([]byte("\x1b"))
				readUntil("esc quit")
				master.Write([]byte("j\r"))
				readUntil("RESULT beta KILLS 0")
			}
			if err := command.Wait(); err != nil {
				t.Fatal(err)
			}
			after, err := fenTerminalState(int(slave.Fd()))
			if err != nil || after.Lflag != before.Lflag || after.Iflag != before.Iflag {
				t.Fatal("terminal not restored", err)
			}
		})
	}
}
