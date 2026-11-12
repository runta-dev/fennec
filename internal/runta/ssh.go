package runta

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fennec/internal/launchprofile"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Connect provisions a device-specific key and verifies the SSH gateway against
// the host key returned by the authenticated HTTPS API. No host-key bypass.
type SSHConnectionConfig struct {
	Address   string
	Config    *ssh.ClientConfig
	HostKey   ssh.PublicKey
	ClientKey ssh.PublicKey
	NoDelay   bool
}

func (c *Client) PrepareSSH(ctx context.Context, id, directory string) (*SSHConnectionConfig, error) {
	return c.prepareSSH(ctx, id, directory, false)
}

func (c *Client) PrepareOwnedSSH(ctx context.Context, id, directory string) (*SSHConnectionConfig, error) {
	return c.prepareSSH(ctx, id, directory, true)
}

func (c *Client) prepareSSH(ctx context.Context, id, directory string, owned bool) (*SSHConnectionConfig, error) {
	defer launchprofile.Begin("ssh.provision")()
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "runtime_key")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, private, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		block, e := ssh.MarshalPrivateKey(private, "Fennec runtime connector")
		if e != nil {
			return nil, e
		}
		data = pem.EncodeToMemory(block)
		file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = file.Write(data)
		file.Close()
		if e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	type key struct {
		ID     string `json:"id"`
		Public string `json:"public_key"`
	}
	var keys struct {
		Data []key `json:"data"`
	}
	var attached struct {
		Data []key `json:"data"`
	}
	var host struct {
		Data struct {
			Public string `json:"public_key"`
		} `json:"data"`
	}
	type query struct {
		path   string
		output any
	}
	queries := []query{
		{"/v2/ssh-keys", &keys},
		{"/v2/runtimes/" + url.PathEscape(id) + "/ssh-keys", &attached},
		{"/v2/ssh-host-key", &host},
	}
	var identity struct {
		Data struct {
			User string `json:"user_id"`
		} `json:"data"`
	}
	var computer struct {
		Data Runtime `json:"data"`
	}
	if owned {
		queries = append(queries, query{"/v2/me", &identity}, query{"/v2/runtimes/" + url.PathEscape(id), &computer})
	}
	failures := make([]error, len(queries))
	var pending sync.WaitGroup
	for index, query := range queries {
		pending.Add(1)
		go func(index int, path string, output any) {
			defer pending.Done()
			failures[index] = c.request(ctx, "GET", path, nil, "", output)
		}(index, query.path, query.output)
	}
	pending.Wait()
	for _, failure := range failures {
		if failure != nil {
			return nil, failure
		}
	}
	if owned {
		if identity.Data.User == "" || computer.Data.ID != id || computer.Data.Owner != identity.Data.User || computer.Data.Environment["FENNEC_PURPOSE"] != "shared-v1" {
			return nil, fmt.Errorf("cloud computer ownership mismatch")
		}
		if computer.Data.Status != "running" || computer.Data.Degraded {
			return nil, fmt.Errorf("cloud computer is not ready")
		}
	}
	keyID := ""
	for _, k := range keys.Data {
		if strings.TrimSpace(k.Public) == public {
			keyID = k.ID
			break
		}
	}
	if keyID == "" {
		var created struct {
			Data key `json:"data"`
		}
		err = c.request(ctx, "POST", "/v2/ssh-keys", map[string]string{"display_name": "Fennec device", "public_key": public}, "", &created)
		if err != nil {
			return nil, err
		}
		keyID = created.Data.ID
	}
	if keyID == "" {
		return nil, fmt.Errorf("missing SSH key ID")
	}
	found := false
	for _, k := range attached.Data {
		if k.ID == keyID {
			found = true
		}
	}
	if !found {
		runtime, e := c.Get(ctx, id)
		if e != nil {
			return nil, e
		}
		err = c.request(ctx, "PUT", fmt.Sprintf("/v2/runtimes/%s/ssh-keys/%s?expected_revision=%d", url.PathEscape(id), url.PathEscape(keyID), runtime.Revision), nil, "", nil)
		if err != nil {
			return nil, err
		}
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host.Data.Public))
	if err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(c.Endpoint)
	hostname := strings.TrimPrefix(endpoint.Hostname(), "api.")
	address := net.JoinHostPort("ssh."+hostname, "22")
	// Persist only the public gateway key obtained over authenticated HTTPS.
	// OpenSSH diagnostics use this file with StrictHostKeyChecking=yes.
	known := "ssh." + hostname + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))) + "\n"
	if err = os.WriteFile(filepath.Join(directory, "known_hosts"), []byte(known), 0600); err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{User: id, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 20 * time.Second}
	return &SSHConnectionConfig{Address: address, Config: config, HostKey: hostKey, ClientKey: signer.PublicKey(), NoDelay: !c.NoSSHNoDelay}, nil
}

func (c *Client) Connect(ctx context.Context, id, directory string) (*ssh.Client, error) {
	defer launchprofile.Begin("ssh.provision_and_connect")()
	cached := cachedSSHConfig(c.Endpoint, id, directory)
	if cached != nil {
		cached.NoDelay = !c.NoSSHNoDelay
	}
	return connectPreparedSSH(ctx, cached, func(ctx context.Context) (*SSHConnectionConfig, error) { return c.PrepareSSH(ctx, id, directory) }, dialPreparedSSH)
}

// ConnectResolved overlaps only a cached, pinned authentication handshake with
// runtime discovery. The hint is not authority: resolve and fresh SSH API checks
// must succeed and agree before the resulting connection is returned.
func (c *Client) ConnectResolved(ctx context.Context, hint, directory string, resolve func(context.Context) (string, error)) (*ssh.Client, error) {
	defer launchprofile.Begin("ssh.resolve_and_connect")()
	cached := cachedSSHConfig(c.Endpoint, hint, directory)
	if cached != nil {
		cached.NoDelay = !c.NoSSHNoDelay
	}
	return connectPreparedSSH(ctx, cached, func(ctx context.Context) (*SSHConnectionConfig, error) {
		id, err := resolve(ctx)
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, fmt.Errorf("runtime identity required")
		}
		return c.PrepareSSH(ctx, id, directory)
	}, dialPreparedSSH)
}

func dialPreparedSSH(ctx context.Context, prepared *SSHConnectionConfig) (*ssh.Client, error) {
	address, config := prepared.Address, prepared.Config
	dialDone := launchprofile.Begin("ssh.tcp")
	raw, err := (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", address)
	dialDone()
	if err != nil {
		return nil, err
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(prepared.NoDelay)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(15 * time.Second)
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	handshakeDone := launchprofile.Begin("ssh.handshake")
	conn, channels, requests, err := ssh.NewClientConn(raw, address, config)
	handshakeDone()
	if err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	if err := ctx.Err(); err != nil {
		raw.Close()
		return nil, err
	}
	return ssh.NewClient(conn, channels, requests), nil
}
