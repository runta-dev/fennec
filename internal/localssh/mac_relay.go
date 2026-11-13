package localssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// Every `ssh <device> command` on the cloud computer opens a new SSH session
// channel, and the Mac must confirm a channel before a command can be sent on
// it: two round trips over a link of about 170 ms, even on a multiplexed
// connection. The device alias therefore reaches the Mac through a relay
// (its ProxyCommand). The ssh client's handshake and channel opens terminate
// at the relay on the cloud computer itself; the relay holds one SSH
// connection to the Mac and keeps session channels open there ahead of use,
// so a command needs only one round trip: the request and its result.
//
// The relay forwards every channel request, data and exit status unchanged.
// The Mac still checks pairing, approval, sandbox and audit for each command,
// and verifies the cloud identity; the relay verifies the Mac's host key.
// Agents keep using plain `ssh <alias> command`, scp, rsync and sftp.

// MacRelayCommand is the fennec-terminal subcommand an alias uses as its
// ProxyCommand: mac-relay <device> <port>.
const MacRelayCommand = "mac-relay"

const (
	relayHostKeyFile    = "relay_host_key"
	relayKnownHostsFile = "relay_known_hosts"
	relayHostKeyAlias   = "fennec-relay"
	// Channels kept open on the Mac; a burst larger than this opens the rest
	// on demand, as plain ssh would.
	relayPoolSize = 2
)

var deviceIDPattern = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

// MacRelayOptions configures one relay. Dial reaches the Mac's SSH service
// through the reverse tunnel.
type MacRelayOptions struct {
	Dial       func(context.Context) (net.Conn, error)
	HostKey    gossh.Signer    // presented to the local ssh client
	Identity   gossh.Signer    // this cloud computer's identity on the Mac
	MacHostKey gossh.PublicKey // the paired Mac's host key
}

// ServeMacRelay runs the relay for one ssh client on its ProxyCommand stdio.
func ServeMacRelay(ctx context.Context, home, device, port string, input io.Reader, output io.WriteCloser) error {
	if !deviceIDPattern.MatchString(device) {
		return fmt.Errorf("invalid device")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("invalid port")
	}
	dir := filepath.Join(home, ".ssh", "fennec")
	readSigner := func(name string) (gossh.Signer, error) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		return gossh.ParsePrivateKey(data)
	}
	hostKey, err := readSigner(relayHostKeyFile)
	if err != nil {
		return fmt.Errorf("relay host key: %w", err)
	}
	identity, err := readSigner(device)
	if err != nil {
		return fmt.Errorf("device identity: %w", err)
	}
	macKey, err := knownHostKey(filepath.Join(dir, device+".known_hosts"), "fennec-"+device)
	if err != nil {
		return err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(number))
	options := MacRelayOptions{HostKey: hostKey, Identity: identity, MacHostKey: macKey, Dial: func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", address)
	}}
	return RunMacRelay(ctx, stdioConn{Reader: input, WriteCloser: output}, options)
}

func knownHostKey(path, host string) (gossh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Mac host key: %w", err)
	}
	for len(data) > 0 {
		_, hosts, key, _, rest, err := gossh.ParseKnownHosts(data)
		if err != nil {
			break
		}
		for _, name := range hosts {
			if name == host {
				return key, nil
			}
		}
		data = rest
	}
	return nil, fmt.Errorf("Mac host key for %s not found", host)
}

// RunMacRelay serves one local ssh client connection until it closes.
func RunMacRelay(ctx context.Context, local net.Conn, options MacRelayOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	identity := options.Identity.PublicKey().Marshal()
	config := &gossh.ServerConfig{PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
		if bytes.Equal(key.Marshal(), identity) {
			return nil, nil
		}
		return nil, fmt.Errorf("unknown key")
	}}
	config.AddHostKey(options.HostKey)
	upstream := &macUpstream{options: options, ctx: ctx, pool: make(chan pooledSession, relayPoolSize)}
	defer upstream.close()
	// Reach the Mac while the local handshake runs.
	go upstream.connect()
	connection, channels, requests, err := gossh.NewServerConn(local, config)
	if err != nil {
		return err
	}
	defer connection.Close()
	go gossh.DiscardRequests(requests) // keepalives get a reply; nothing else is offered
	var sessions sync.WaitGroup
	for channel := range channels {
		sessions.Add(1)
		go func(channel gossh.NewChannel) {
			defer sessions.Done()
			switch channel.ChannelType() {
			case "session":
				upstream.relaySession(channel)
			case "direct-tcpip":
				upstream.relayChannel(channel)
			default:
				channel.Reject(gossh.UnknownChannelType, "not supported by Fennec")
			}
		}(channel)
	}
	cancel()
	upstream.close()
	sessions.Wait()
	return nil
}

type pooledSession struct {
	client   *gossh.Client
	channel  gossh.Channel
	requests <-chan *gossh.Request
}

type macUpstream struct {
	options MacRelayOptions
	ctx     context.Context

	mu      sync.Mutex
	client  *gossh.Client
	dialing chan struct{} // closed when the current dial attempt ends
	err     error
	filling bool
	closed  bool
	pool    chan pooledSession
}

// connect returns the live connection to the Mac, dialing once for all
// waiting sessions.
func (u *macUpstream) connect() (*gossh.Client, error) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil, net.ErrClosed
	}
	if u.client != nil {
		client := u.client
		u.mu.Unlock()
		return client, nil
	}
	if u.dialing != nil {
		wait := u.dialing
		u.mu.Unlock()
		<-wait
		u.mu.Lock()
		client, err := u.client, u.err
		u.mu.Unlock()
		if client == nil && err == nil {
			err = net.ErrClosed
		}
		return client, err
	}
	done := make(chan struct{})
	u.dialing = done
	u.mu.Unlock()
	client, err := u.dial()
	u.mu.Lock()
	u.dialing = nil
	u.err = err
	if err == nil && u.closed {
		client.Close()
		client, err = nil, net.ErrClosed
	}
	u.client = client
	u.mu.Unlock()
	close(done)
	if err != nil {
		return nil, err
	}
	go u.watch(client)
	go u.fill(client)
	return client, nil
}

func (u *macUpstream) dial() (*gossh.Client, error) {
	ctx, cancel := context.WithTimeout(u.ctx, 15*time.Second)
	defer cancel()
	conn, err := u.options.Dial(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	connection, channels, requests, err := gossh.NewClientConn(conn, "fennec-mac", &gossh.ClientConfig{
		User:            "fennec",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(u.options.Identity)},
		HostKeyCallback: gossh.FixedHostKey(u.options.MacHostKey),
	})
	if !stop() {
		if err == nil {
			connection.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return gossh.NewClient(connection, channels, requests), nil
}

// watch keeps the connection alive and forgets it once it ends, so the next
// session dials again.
func (u *macUpstream) watch(client *gossh.Client) {
	ended := make(chan struct{})
	go func() {
		client.Wait()
		close(ended)
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ended:
			u.mu.Lock()
			if u.client == client {
				u.client = nil
			}
			u.mu.Unlock()
			return
		case <-ticker.C:
			go func() {
				reply := make(chan error, 1)
				go func() {
					_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
					reply <- err
				}()
				select {
				case err := <-reply:
					if err != nil {
						client.Close()
					}
				case <-time.After(45 * time.Second):
					client.Close()
				}
			}()
		}
	}
}

// fill keeps relayPoolSize session channels open on the Mac.
func (u *macUpstream) fill(client *gossh.Client) {
	u.mu.Lock()
	if u.filling || u.closed {
		u.mu.Unlock()
		return
	}
	u.filling = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.filling = false
		u.mu.Unlock()
	}()
	for len(u.pool) < cap(u.pool) {
		channel, requests, err := client.OpenChannel("session", nil)
		if err != nil {
			return
		}
		select {
		case u.pool <- pooledSession{client: client, channel: channel, requests: requests}:
		default:
			channel.Close()
			return
		}
	}
}

// session returns an open session channel on the Mac: a waiting one when
// there is one, otherwise a new one.
func (u *macUpstream) session() (gossh.Channel, <-chan *gossh.Request, error) {
	client, err := u.connect()
	if err != nil {
		return nil, nil, err
	}
	for {
		select {
		case pooled := <-u.pool:
			if pooled.client != client {
				pooled.channel.Close()
				continue
			}
			go u.fill(client)
			return pooled.channel, pooled.requests, nil
		default:
			go u.fill(client)
			return client.OpenChannel("session", nil)
		}
	}
}

func (u *macUpstream) close() {
	u.mu.Lock()
	u.closed = true
	client := u.client
	u.client = nil
	u.mu.Unlock()
	if client != nil {
		client.Close()
	}
	for {
		select {
		case pooled := <-u.pool:
			pooled.channel.Close()
		default:
			return
		}
	}
}

// relaySession carries one ssh session to the Mac. The local channel is
// accepted at once, so the client's requests are already on their way while
// a channel on the Mac is taken.
func (u *macUpstream) relaySession(incoming gossh.NewChannel) {
	local, localRequests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer local.Close()
	remote, remoteRequests, err := u.session()
	if err != nil {
		// Answer like a server whose command failed, so ssh prints the reason
		// and exits 255 as it does for an unreachable host.
		for request := range localRequests {
			if request.WantReply {
				request.Reply(true, nil)
			}
			switch request.Type {
			case "exec", "shell", "subsystem":
				go gossh.DiscardRequests(localRequests)
				io.WriteString(local.Stderr(), "Fennec: this Mac is not reachable: "+err.Error()+"\n")
				local.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{255}))
				local.CloseWrite()
				return
			}
		}
		return
	}
	defer remote.Close()
	relayStreams(local, localRequests, remote, remoteRequests)
}

// relayChannel carries a forwarded TCP connection (ssh -L/-D).
func (u *macUpstream) relayChannel(incoming gossh.NewChannel) {
	client, err := u.connect()
	if err != nil {
		incoming.Reject(gossh.ConnectionFailed, "this Mac is not reachable")
		return
	}
	remote, remoteRequests, err := client.OpenChannel(incoming.ChannelType(), incoming.ExtraData())
	if err != nil {
		var open *gossh.OpenChannelError
		if errors.As(err, &open) {
			incoming.Reject(open.Reason, open.Message)
		} else {
			incoming.Reject(gossh.ConnectionFailed, "this Mac is not reachable")
		}
		return
	}
	defer remote.Close()
	local, localRequests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer local.Close()
	relayStreams(local, localRequests, remote, remoteRequests)
}

// relayStreams copies data, stderr, EOF and channel requests both ways until
// the Mac closes the channel or the client goes away.
func relayStreams(local gossh.Channel, localRequests <-chan *gossh.Request, remote gossh.Channel, remoteRequests <-chan *gossh.Request) {
	go func() {
		// Requests keep their order; one awaiting a reply holds the next.
		for request := range localRequests {
			ok, err := remote.SendRequest(request.Type, request.WantReply, request.Payload)
			if request.WantReply {
				request.Reply(ok && err == nil, nil)
			}
		}
		remote.Close() // the client is gone
	}()
	go func() {
		io.Copy(remote, local)
		remote.CloseWrite()
	}()
	var output sync.WaitGroup
	output.Add(2)
	go func() { defer output.Done(); io.Copy(local, remote) }()
	go func() { defer output.Done(); io.Copy(local.Stderr(), remote.Stderr()) }()
	for request := range remoteRequests { // exit-status, exit-signal, keepalives
		ok, err := local.SendRequest(request.Type, request.WantReply, request.Payload)
		if request.WantReply {
			request.Reply(ok && err == nil, nil)
		}
	}
	output.Wait()
	local.CloseWrite()
}

// EnsureRelayHostKey creates the relay's host key and the known_hosts file
// the alias uses to verify it.
func EnsureRelayHostKey(dir string) error {
	path := filepath.Join(dir, relayHostKeyFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		block, err := gossh.MarshalPrivateKey(private, "Fennec Mac relay")
		if err != nil {
			return err
		}
		data = pem.EncodeToMemory(block)
		if err := atomicAliasWrite(path, data); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return err
	}
	known := relayHostKeyAlias + " " + string(gossh.MarshalAuthorizedKey(signer.PublicKey()))
	if existing, err := os.ReadFile(filepath.Join(dir, relayKnownHostsFile)); err == nil && string(existing) == known {
		return nil
	}
	return atomicAliasWrite(filepath.Join(dir, relayKnownHostsFile), []byte(known))
}

// stdioConn presents ProxyCommand stdio as a connection.
type stdioConn struct {
	io.Reader
	io.WriteCloser
}

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

func (stdioConn) LocalAddr() net.Addr              { return stdioAddr{} }
func (stdioConn) RemoteAddr() net.Addr             { return stdioAddr{} }
func (stdioConn) SetDeadline(time.Time) error      { return nil }
func (stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (stdioConn) SetWriteDeadline(time.Time) error { return nil }
func (conn stdioConn) Close() error                { return conn.WriteCloser.Close() }
