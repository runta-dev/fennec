package runta

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Read SSH key metadata while the runtime list/owner request is in flight. The
// gate is mandatory before prepare makes any mutation or returns usable pins.
func resolvePreparedSSH(ctx context.Context, hint string, resolve func(context.Context) (string, error), prepare func(context.Context, string, func() error) (*SSHConnectionConfig, error)) (*SSHConnectionConfig, error) {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		config *SSHConnectionConfig
		err    error
	}
	var id string
	var resolveErr error
	resolved := make(chan struct{})
	ready := make(chan result, 1)
	if hint != "" {
		go func() {
			config, err := prepare(work, hint, func() error {
				select {
				case <-work.Done():
					return work.Err()
				case <-resolved:
				}
				if resolveErr != nil {
					return resolveErr
				}
				if id != hint {
					return fmt.Errorf("runtime hint no longer matches verified owner")
				}
				return nil
			})
			ready <- result{config, err}
		}()
	}
	id, resolveErr = resolve(work)
	close(resolved)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if id == "" {
		return nil, fmt.Errorf("runtime identity required")
	}
	if hint != "" && id == hint {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-ready:
			return result.config, result.err
		}
	}
	// A stale hint is never authoritative. Cancel its reads and use only the
	// freshly resolved runtime; commands and key mutations were still gated.
	cancel()
	return prepare(ctx, id, nil)
}

// Only reuse the exact single-host entry written by PrepareSSH. Missing or
// unfamiliar local state uses the ordinary API-first path.
func cachedSSHConfig(endpoint, id, directory string) *SSHConnectionConfig {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || id == "" {
		return nil
	}
	host := "ssh." + strings.TrimPrefix(u.Hostname(), "api.")
	known, err := os.ReadFile(filepath.Join(directory, "known_hosts"))
	if err != nil || len(known) > 16384 {
		return nil
	}
	marker, hosts, key, _, rest, err := ssh.ParseKnownHosts(known)
	if err != nil || marker != "" || len(hosts) != 1 || hosts[0] != host || len(bytes.TrimSpace(rest)) != 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(directory, "runtime_key"))
	if err != nil || len(data) > 16384 {
		return nil
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil
	}
	return &SSHConnectionConfig{Address: net.JoinHostPort(host, "22"), HostKey: key, ClientKey: signer.PublicKey(), NoDelay: true, Config: &ssh.ClientConfig{User: id, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(key), Timeout: 20 * time.Second}}
}

func sameSSHIdentity(a, b *SSHConnectionConfig) bool {
	return a != nil && b != nil && a.Config != nil && b.Config != nil && a.HostKey != nil && b.HostKey != nil && a.ClientKey != nil && b.ClientKey != nil && a.Address == b.Address && a.Config.User == b.Config.User && bytes.Equal(a.HostKey.Marshal(), b.HostKey.Marshal()) && bytes.Equal(a.ClientKey.Marshal(), b.ClientKey.Marshal())
}

// Speculation authenticates only: no session or command can be opened before
// the fresh API check succeeds and agrees with every cached identity component.
func connectPreparedSSH(ctx context.Context, cached *SSHConnectionConfig, prepare func(context.Context) (*SSHConnectionConfig, error), dial func(context.Context, *SSHConnectionConfig) (*ssh.Client, error)) (*ssh.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached == nil {
		config, err := prepare(ctx)
		if err != nil {
			return nil, err
		}
		return dial(ctx, config)
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		client *ssh.Client
		err    error
	}
	done := make(chan result, 1)
	go func() { client, err := dial(work, cached); done <- result{client, err} }()
	config, err := prepare(work)
	if err != nil || !sameSSHIdentity(cached, config) {
		cancel()
	}
	attempt := <-done
	if err != nil {
		if attempt.client != nil {
			attempt.client.Close()
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		if attempt.client != nil {
			attempt.client.Close()
		}
		return nil, err
	}
	if attempt.err == nil && sameSSHIdentity(cached, config) {
		return attempt.client, nil
	}
	if attempt.client != nil {
		attempt.client.Close()
	}
	return dial(ctx, config)
}
