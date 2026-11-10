package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestLiveTerminalBinaryInstall(t *testing.T) {
	if os.Getenv("FENNEC_LIVE_TERMINAL_INSTALL") != "1" {
		t.Skip("explicit live installation required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, ".config/fennec")
	data, err := os.ReadFile(filepath.Join(directory, "takeover-preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	var preferences struct {
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.Unmarshal(data, &preferences); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(directory, "cloud/runtime_key"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	check, err := knownhosts.New(filepath.Join(directory, "cloud/known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", "ssh.runta.com:22", &ssh.ClientConfig{User: preferences.RuntimeID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: check, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		if err := InstallTerminalBinary(ctx, client, os.Getenv("FENNEC_TERMINAL_ARTIFACTS")); err != nil {
			t.Fatal(err)
		}
	}
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.Stdin = strings.NewReader("")
	output, err := session.Output(`"$HOME/.local/fennec/bin/fennec-terminal" serve`)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}
	if json.Unmarshal(output, &frame) != nil || frame.Type != "ready" || frame.Version != 2 {
		t.Fatal("installed service did not negotiate protocol")
	}
}
