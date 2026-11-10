package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/creack/pty"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	binary := flag.String("binary", "", "Fennec daemon binary")
	mode := flag.String("mode", "cold", "cold or warm")
	samples := flag.Int("samples", 3, "samples")
	flag.Parse()
	if *binary == "" || (*mode != "cold" && *mode != "warm") {
		return fmt.Errorf("binary and cold/warm mode required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".config/fennec")
	marker, err := os.OpenFile(filepath.Join(root, "launch-profile.enabled"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	marker.Close()
	defer os.Remove(marker.Name())
	for sample := 0; sample < *samples; sample++ {
		if err := measure(*binary, *mode, root, sample+1); err != nil {
			return err
		}
	}
	return nil
}

func measure(binary, mode, root string, sample int) error {
	directory, err := os.MkdirTemp("", "fennec-launch-profile-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run-agent", "codex", "--no-alt-screen")
	cold := "0"
	if mode == "cold" {
		cold = "1"
	}
	command.Env = append(os.Environ(), "TERM=xterm-256color", "FENNEC_COLD_START="+cold)
	command.Dir = directory
	started := time.Now()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return err
	}
	defer terminal.Close()
	var output bytes.Buffer
	buffer := make([]byte, 32768)
	ansi := regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	responses := map[string]string{"\x1b]10;?": "\x1b]10;rgb:cccc/cccc/cccc\x1b\\", "\x1b]11;?": "\x1b]11;rgb:1818/1818/1818\x1b\\", "\x1b]12;?": "\x1b]12;rgb:cccc/cccc/cccc\x1b\\", "\x1b[16t": "\x1b[6;16;8t", "\x1b[c": "\x1b[?1;2c", "\x1b[6n": "\x1b[1;1R"}
	screen := time.Duration(0)
	for {
		length, readErr := terminal.Read(buffer)
		if length > 0 {
			output.Write(buffer[:length])
			for query, response := range responses {
				if bytes.Contains(buffer[:length], []byte(query)) {
					terminal.Write([]byte(response))
				}
			}
			plain := ansi.ReplaceAll(output.Bytes(), nil)
			if screen == 0 && bytes.Contains(plain, []byte("›")) && bytes.Contains(plain, []byte("Ask Codex")) {
				screen = time.Since(started)
				terminal.Write([]byte{4})
			}
		}
		if readErr != nil {
			break
		}
	}
	waitErr := command.Wait()
	var events []map[string]any
	file, err := os.Open(filepath.Join(root, "launch-profile.jsonl"))
	if err == nil {
		decoder := json.NewDecoder(file)
		for {
			var event map[string]any
			err := decoder.Decode(&event)
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			if event["pid"] == float64(command.Process.Pid) {
				events = append(events, event)
			}
		}
		file.Close()
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": mode, "sample": sample, "screen_ms": float64(screen.Microseconds()) / 1000, "exit_code": command.ProcessState.ExitCode(), "spinner_visible": bytes.Contains(output.Bytes(), []byte("⠋")), "events": events})
	if waitErr != nil {
		return fmt.Errorf("test launch did not finish: %w; inspect its receipt before retrying", waitErr)
	}
	if screen == 0 {
		return fmt.Errorf("usable composer was not observed")
	}
	return nil
}
