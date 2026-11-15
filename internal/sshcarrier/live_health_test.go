package sshcarrier

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Explicit integration test. A cloud observer pauses only this test worker's
// newly created echo process after ISOLATED_WORKER_READY, then resumes it.
// Existing Fennec daemon connections and user terminals are not interrupted.
func TestLiveCarrierRetiresBlockedRuntimeEcho(t *testing.T) {
	path := os.Getenv("FENNEC_LIVE_CARRIER_CONFIG")
	if path == "" {
		t.Skip("requires an explicit Mac transport config and cloud fault observer")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	signer, err := config.signer()
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePublicKey(config.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	directory, err := os.MkdirTemp("", "carrier-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "test.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, signer, func(ctx context.Context) (*ssh.Client, error) {
			raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", config.Address)
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { raw.Close() })
			defer stop()
			conn, channels, requests, err := ssh.NewClientConn(raw, config.Address, &ssh.ClientConfig{User: config.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host)})
			if err != nil {
				raw.Close()
				return nil, err
			}
			return ssh.NewClient(conn, channels, requests), nil
		}, time.Minute)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned live worker did not stop")
		}
	}()
	command := func(client *ssh.Client) error {
		session, err := client.NewSession()
		if err != nil {
			return err
		}
		defer session.Close()
		output, err := session.Output("printf carrier-live-readback")
		if err != nil {
			return err
		}
		if string(output) != "carrier-live-readback" {
			t.Fatal("remote readback mismatch")
		}
		return nil
	}
	first, err := dialLocal(ctx, socket, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = command(first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	t.Log("ISOLATED_WORKER_READY")
	ended := make(chan error, 1)
	go func() { ended <- first.Wait() }()
	select {
	case <-ended:
	case <-time.After(12 * time.Second):
		t.Fatal("runtime echo failure was not retired")
	}
	t.Log("Failed upstream retired; acquiring replacement")
	replacement, err := dialLocal(ctx, socket, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err = command(replacement); err != nil {
		t.Fatal(err)
	}
	t.Log("Replacement runtime command and readback confirmed")
}
