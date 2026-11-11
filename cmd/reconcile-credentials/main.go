// Temporary deployment-independent repair using the same production Go merger.
// Credential payloads flow only through SSH stdin; stdout contains counts only.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/setup"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	receiver := flag.String("receiver", "", "Linux receiver artifact")
	flag.Parse()
	if *receiver == "" {
		return fmt.Errorf("receiver required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var state struct {
		Account struct {
			Status string `json:"status"`
		} `json:"account"`
		Runtime struct {
			ID string `json:"runtime_id"`
		} `json:"runtime"`
	}
	if err := (control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}).Call(ctx, "state.get", nil, &state); err != nil {
		return err
	}
	if state.Account.Status != "authorized" || state.Runtime.ID == "" {
		return fmt.Errorf("authenticated runtime required")
	}
	payload, err := setup.GatherCredentialPayload(home)
	if err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cloud := filepath.Join(home, ".config/fennec/cloud")
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(cloud, "known_hosts"), "-i", filepath.Join(cloud, "runtime_key"), state.Runtime.ID + "@ssh.runta.com"}
	command := func(script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/usr/bin/ssh", append(append([]string{}, args...), script)...)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	remote := "/tmp/fennec-credential-receiver-" + hex.EncodeToString(nonce[:])
	file, err := os.Open(*receiver)
	if err != nil {
		return err
	}
	upload := command("umask 077; cat > " + remote + " && chmod 700 " + remote)
	upload.Stdin = file
	err = upload.Run()
	file.Close()
	if err != nil {
		return fmt.Errorf("receiver upload failed")
	}
	defer command("rm -f " + remote).Run()
	apply := command(remote)
	apply.Stdin = bytes.NewReader(data)
	raw, err := apply.Output()
	if err != nil {
		return fmt.Errorf("credential reconciliation failed; existing remote values preserved")
	}
	var report setup.CredentialMigrationReport
	if len(raw) > 4096 || json.Unmarshal(raw, &report) != nil {
		return fmt.Errorf("invalid reconciliation report")
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
