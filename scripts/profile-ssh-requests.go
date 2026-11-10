package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func main() {
	home, err := os.UserHomeDir()
	must(err)
	directory := filepath.Join(home, ".config/fennec")
	data, err := os.ReadFile(filepath.Join(directory, "takeover-preferences.json"))
	must(err)
	var preferences struct {
		RuntimeID string `json:"runtime_id"`
	}
	must(json.Unmarshal(data, &preferences))
	key, err := os.ReadFile(filepath.Join(directory, "cloud/runtime_key"))
	must(err)
	signer, err := ssh.ParsePrivateKey(key)
	must(err)
	check, err := knownhosts.New(filepath.Join(directory, "cloud/known_hosts"))
	must(err)
	client, err := ssh.Dial("tcp", "ssh.runta.com:22", &ssh.ClientConfig{User: preferences.RuntimeID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: check, Timeout: 20 * time.Second})
	must(err)
	defer client.Close()
	for _, reply := range []bool{true, false, true, false} {
		started := time.Now()
		channel, requests, err := client.OpenChannel("session", nil)
		must(err)
		opened := time.Since(started)
		status := make(chan int, 1)
		go func() {
			code := -1
			for request := range requests {
				if request.Type == "exit-status" && len(request.Payload) == 4 {
					code = int(binary.BigEndian.Uint32(request.Payload))
				}
				if request.WantReply {
					request.Reply(false, nil)
				}
			}
			status <- code
		}()
		pty := struct {
			Term                         string
			Columns, Rows, Width, Height uint32
			Modes                        string
		}{"xterm-256color", 120, 40, 0, 0, "\x00"}
		ok, err := channel.SendRequest("pty-req", reply, ssh.Marshal(pty))
		must(err)
		if reply && !ok {
			panic("PTY rejected")
		}
		ok, err = channel.SendRequest("exec", reply, ssh.Marshal(struct{ Command string }{"true"}))
		must(err)
		if reply && !ok {
			panic("exec rejected")
		}
		go io.Copy(io.Discard, channel.Stderr())
		io.Copy(io.Discard, channel)
		channel.Close()
		code := <-status
		json.NewEncoder(os.Stdout).Encode(map[string]any{"want_reply": reply, "channel_open_ms": float64(opened.Microseconds()) / 1000, "complete_ms": float64(time.Since(started).Microseconds()) / 1000, "exit_code": code})
		if code != 0 {
			panic("remote command did not complete successfully")
		}
	}
}

func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("SSH benchmark failed: %v", err))
	}
}
