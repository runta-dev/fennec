package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/launcher"
	cloudruntime "fennec/internal/runtime"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	binary := flag.String("binary", "", "Fennec daemon binary")
	samples := flag.Int("samples", 3, "resume samples")
	debugStall := flag.Bool("debug-stall", false, "dump the owned test client's stack if detach stalls")
	flag.Parse()
	if *binary == "" {
		return fmt.Errorf("binary required")
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
	data, err := os.ReadFile(filepath.Join(root, "takeover-preferences.json"))
	if err != nil {
		return err
	}
	var preferences struct {
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.Unmarshal(data, &preferences); err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(root, "cloud/runtime_key"))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return err
	}
	check, err := knownhosts.New(filepath.Join(root, "cloud/known_hosts"))
	if err != nil {
		return err
	}
	client, err := ssh.Dial("tcp", "ssh.runta.com:22", &ssh.ClientConfig{User: preferences.RuntimeID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: check, Timeout: 20 * time.Second})
	if err != nil {
		return err
	}
	defer client.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	request := cloudruntime.LocalLaunch{ID: id, Agent: "codex", LocalDirectory: "/tmp/fennec-resume-profile", Device: "1234abcd", Arguments: []string{"--no-alt-screen"}}
	payload, _ := json.Marshal(request)
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	session.Stdin = bytes.NewReader(payload)
	raw, err := session.Output(`"$HOME/.local/fennec/bin/fennec-terminal" launch`)
	session.Close()
	if err != nil {
		return err
	}
	var terminal cloudruntime.LocalTerminal
	if json.Unmarshal(raw, &terminal) != nil || terminal.ID != id {
		return fmt.Errorf("invalid test terminal")
	}
	defer func() {
		get, err := client.NewSession()
		if err != nil {
			return
		}
		data, err := get.Output(`"$HOME/.local/bin/herdr-core" get ` + terminal.Address)
		get.Close()
		if err != nil {
			return
		}
		var current cloudruntime.LocalTerminal
		if json.Unmarshal(data, &current) != nil || current.Labels["fennec.session"] != id {
			return
		}
		if current.State != "finished" {
			kill, err := client.NewSession()
			if err == nil {
				kill.Run(`"$HOME/.local/bin/herdr-core" kill ` + terminal.Address)
				kill.Close()
			}
		}
	}()
	query := func(operation string) (bool, error) {
		conn, err := net.DialTimeout("unix", filepath.Join(root, "ipc/sessions.sock"), time.Second)
		if err != nil {
			return false, err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := json.NewEncoder(conn).Encode(map[string]string{"operation": operation}); err != nil {
			return false, err
		}
		var reply struct {
			Sessions []struct{ Receipt launcher.LaunchReceipt } `json:"sessions"`
		}
		if err := json.NewDecoder(conn).Decode(&reply); err != nil {
			return false, err
		}
		for _, value := range reply.Sessions {
			if value.Receipt.SessionID == id {
				return true, nil
			}
		}
		return false, nil
	}
	if _, err := query("refresh"); err != nil {
		return err
	}
	deadline := time.Now().Add(40 * time.Second)
	for {
		found, err := query("list")
		if err != nil {
			return err
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("test session did not appear in cache")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for sample := 0; sample < *samples; sample++ {
		if err := measure(*binary, id, root, sample+1, *debugStall); err != nil {
			return err
		}
	}
	return nil
}

func measure(binary, id, root string, sample int, debugStall bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "fen", "resume", id)
	command.Env = append(os.Environ(), "TERM=xterm-256color")
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
			if screen == 0 && bytes.Contains(plain, []byte("›")) && (bytes.Contains(plain, []byte("OpenAI Codex")) || bytes.Contains(plain, []byte("Ask Codex"))) {
				screen = time.Since(started)
				terminal.Write([]byte{28})
				if debugStall {
					time.AfterFunc(3*time.Second, func() { command.Process.Signal(syscall.SIGQUIT) })
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if err := command.Wait(); err != nil {
		plain := ansi.ReplaceAll(output.Bytes(), nil)
		limit := 1200
		if debugStall {
			limit = 16000
		}
		if len(plain) > limit {
			plain = plain[len(plain)-limit:]
		}
		return fmt.Errorf("resume did not exit cleanly: %w; pid=%d screen_ms=%.2f; diagnostic test output=%q", err, command.Process.Pid, float64(screen.Microseconds())/1000, string(plain))
	}
	if screen == 0 {
		return fmt.Errorf("resume TUI prompt was not observed")
	}
	var events []map[string]any
	data, _ := os.ReadFile(filepath.Join(root, "launch-profile.jsonl"))
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var event map[string]any
		if json.Unmarshal(line, &event) == nil && event["pid"] == float64(command.Process.Pid) {
			events = append(events, event)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"sample": sample, "resume_screen_ms": float64(screen.Microseconds()) / 1000, "spinner_visible": bytes.Contains(output.Bytes(), []byte("⠋")), "events": events})
}
