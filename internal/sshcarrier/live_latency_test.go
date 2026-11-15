package sshcarrier

import (
	"context"
	"encoding/json"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit read-only diagnostic against an already authorized running carrier.
// Only aggregate timings are logged; key material and command output stay private.
func TestLiveCarrierCommandLatency(t *testing.T) {
	path := os.Getenv("FENNEC_LIVE_LATENCY_CONFIG")
	if path == "" {
		t.Skip("requires an explicit running Mac carrier")
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := dialLocal(ctx, filepath.Join(filepath.Dir(path), "transport-"+config.key()+".sock"), signer)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	for _, command := range []string{"exec /bin/echo latency-probe", `exec "$HOME/.local/bin/herdr-core" server status`, `exec /usr/bin/curl --fail --silent --max-time 2 http://127.0.0.1:3425/v1/models`} {
		for i := 0; i < 3; i++ {
			start := time.Now()
			channel, requests, err := client.OpenChannel("session", nil)
			if err != nil {
				t.Fatal(err)
			}
			opened := time.Now()
			go ssh.DiscardRequests(requests)
			go io.Copy(io.Discard, channel.Stderr())
			_, err = channel.SendRequest("exec", false, ssh.Marshal(struct{ Command string }{command}))
			if err != nil {
				t.Fatal(err)
			}
			var one [1]byte
			_, err = io.ReadFull(channel, one[:])
			first := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(io.Discard, channel)
			if err != nil {
				t.Fatal(err)
			}
			channel.Close()
			t.Logf("command=%q sample=%d open_ms=%.3f first_byte_ms=%.3f total_ms=%.3f", command, i, float64(opened.Sub(start).Microseconds())/1000, float64(first.Sub(start).Microseconds())/1000, float64(time.Since(start).Microseconds())/1000)
		}
	}
}
