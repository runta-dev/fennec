package runta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestProvisioningCannotWriteBeforeResolutionGate(t *testing.T) {
	var reads, mutations atomic.Int32
	key := testSSHConfig(t).HostKey
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			http.Error(w, "unexpected mutation", 500)
			return
		}
		reads.Add(1)
		if r.URL.Path == "/v2/ssh-host-key" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"public_key": string(ssh.MarshalAuthorizedKey(key))}})
		} else {
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer api.Close()
	directory := t.TempDir()
	denied := errors.New("runtime not verified")
	client := &Client{Endpoint: api.URL, Token: "test"}
	config, err := client.prepareSSH(context.Background(), "hint", directory, false, func() error { return denied })
	if config != nil || !errors.Is(err, denied) || reads.Load() != 3 || mutations.Load() != 0 {
		t.Fatalf("gate failed: config=%v error=%v reads=%d mutations=%d", config, err, reads.Load(), mutations.Load())
	}
	if _, err := os.Stat(filepath.Join(directory, "known_hosts")); !os.IsNotExist(err) {
		t.Fatal("unverified host pin published")
	}
}

func TestKeyReadsOverlapOwnerResolutionButWritesStayGated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reads := make(chan struct{})
	validated := make(chan struct{})
	var wrote atomic.Bool
	config := testSSHConfig(t)
	got, err := resolvePreparedSSH(ctx, "runtime", func(ctx context.Context) (string, error) {
		select {
		case <-reads:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if wrote.Load() {
			t.Error("key written before owner validation")
		}
		close(validated)
		return "runtime", nil
	}, func(ctx context.Context, id string, verify func() error) (*SSHConnectionConfig, error) {
		close(reads)
		if err := verify(); err != nil {
			return nil, err
		}
		select {
		case <-validated:
		default:
			t.Error("gate returned before ownership")
		}
		wrote.Store(true)
		return config, nil
	})
	if err != nil || got != config || !wrote.Load() {
		t.Fatalf("configuration=%v error=%v", got, err)
	}
}

func TestChangedOrRejectedRuntimeCannotMutateHint(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{true: "rejected", false: "changed"}[rejected], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reads := make(chan struct{})
			hintDone := make(chan struct{})
			var writes atomic.Int32
			denied := errors.New("not owned")
			config := testSSHConfig(t)
			got, err := resolvePreparedSSH(ctx, "stale", func(ctx context.Context) (string, error) {
				select {
				case <-reads:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				if rejected {
					return "", denied
				}
				return "fresh", nil
			}, func(ctx context.Context, id string, verify func() error) (*SSHConnectionConfig, error) {
				if id == "stale" {
					defer close(hintDone)
					close(reads)
					if err := verify(); err != nil {
						return nil, err
					}
					t.Error("stale hint passed gate")
				}
				writes.Add(1)
				return config, nil
			})
			select {
			case <-hintDone:
			case <-ctx.Done():
				t.Fatal("stale reads leaked")
			}
			if rejected {
				if !errors.Is(err, denied) || got != nil || writes.Load() != 0 {
					t.Fatalf("rejected resolution escaped: %v %d", err, writes.Load())
				}
			} else if err != nil || got != config || writes.Load() != 1 {
				t.Fatalf("fresh resolution failed: %v %d", err, writes.Load())
			}
		})
	}
}
