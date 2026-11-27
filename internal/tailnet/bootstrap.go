package tailnet

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Errors the add dialog explains for a password or private key (FEN-123).
const (
	ErrKeyInvalid    = "key_invalid"    // not a private key we can read
	ErrKeyPassphrase = "key_passphrase" // encrypted key, passphrase missing or wrong
)

// authorize signs in to user@host:port once with the password or private key
// the user typed and adds the cloud computer's public key to that account's
// authorized_keys, so later sign-ins use the key. The credential lives only
// in this call: it is never written to disk or logged.
func authorize(ctx context.Context, p paths, host string, port int, r Request, windows bool) string {
	key, err := ensureKey(p)
	if err != nil {
		return ErrSSHFailed
	}
	commands := AuthorizeCommands(key)
	if commands == nil {
		return ErrSSHFailed
	}
	var methods []ssh.AuthMethod
	if r.PrivateKey != "" {
		var signer ssh.Signer
		if r.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(r.PrivateKey), []byte(r.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(r.PrivateKey))
		}
		var missing *ssh.PassphraseMissingError
		switch {
		case errors.As(err, &missing), errors.Is(err, x509.IncorrectPasswordError):
			return ErrKeyPassphrase
		case err != nil:
			return ErrKeyInvalid
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if r.Password != "" {
		password := r.Password
		methods = append(methods, ssh.Password(password), ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = password
			}
			return answers, nil
		}))
	}
	if len(methods) == 0 {
		return ErrAuthFailed
	}

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := dial(ctx, p, host, port)
	if err != nil {
		return ErrUnreachable
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	hostKeyChanged := false
	config := &ssh.ClientConfig{
		User:            r.User,
		Auth:            methods,
		HostKeyCallback: acceptNewHostKey(p, &hostKeyChanged),
		Timeout:         15 * time.Second,
	}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		conn.Close()
		// tailscale nc says why it couldn't connect on its stderr.
		text := strings.ToLower(err.Error() + " " + dialError(conn))
		switch {
		case hostKeyChanged:
			return ErrHostKeyChange
		case strings.Contains(text, "unable to authenticate") || strings.Contains(text, "no supported methods"):
			return ErrAuthFailed
		case strings.Contains(text, "refused"):
			return ErrRefused
		case ctx.Err() != nil || strings.Contains(text, "eof") || strings.Contains(text, "timed out") || strings.Contains(text, "unreachable") || strings.Contains(text, "no route"):
			return ErrUnreachable
		}
		return ErrSSHFailed
	}
	client := ssh.NewClient(sshConn, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return ErrSSHFailed
	}
	defer session.Close()
	// The POSIX command without its Remote Login check: we are signed in
	// over SSH already. The probe that follows checks the key really works.
	command, _, _ := strings.Cut(commands["posix"], ` && { [ "$(uname)" != Darwin ]`)
	if windows {
		command = "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(commands["windows"])
	}
	if err = session.Run(command); err != nil {
		return ErrSSHFailed
	}
	return ""
}

// tailscaleSSHCheck signs in to a Tailscale SSH server with no credentials,
// as the cloud computer's own ssh does. Tailscale SSH decides from the tailnet
// policy: accept, deny, or "check", which holds the sign-in and sends a
// banner with a login.tailscale.com URL to approve it. It returns "" when the
// sign-in is accepted, ErrSSHCheck and that URL, or another error code.
func tailscaleSSHCheck(ctx context.Context, p paths, host, user string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := dial(ctx, p, host, 22)
	if err != nil {
		return ErrUnreachable, ""
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var url string
	hostKeyChanged := false
	config := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: acceptNewHostKey(p, &hostKeyChanged),
		BannerCallback: func(message string) error {
			if found := checkURL.FindString(message); found != "" {
				url = found
				cancel() // the check waits for a person; don't hold the sign-in open
			}
			return nil
		},
		Timeout: 15 * time.Second,
	}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, net.JoinHostPort(host, "22"), config)
	switch {
	case err == nil:
		ssh.NewClient(sshConn, channels, requests).Close()
		return "", ""
	case url != "":
		return ErrSSHCheck, url
	case hostKeyChanged:
		return ErrHostKeyChange, ""
	case strings.Contains(strings.ToLower(err.Error()), "unable to authenticate"):
		return ErrAuthFailed, ""
	case ctx.Err() != nil:
		return ErrUnreachable, ""
	}
	return ErrSSHFailed, ""
}

var checkURL = regexp.MustCompile(`https://login\.tailscale\.com/[^\s"'<>]+`)

// encodePowerShell is the UTF-16LE base64 form -EncodedCommand expects, so
// the script needs no quoting for cmd.exe or PowerShell.
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, 0, len(units)*2)
	for _, u := range units {
		data = append(data, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(data)
}

// acceptNewHostKey trusts a host the first time, like OpenSSH's
// StrictHostKeyChecking=accept-new, using the same known_hosts file as the
// later ssh sign-ins.
func acceptNewHostKey(p paths, changed *bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		file := p.knownHosts()
		if _, err := os.Stat(file); err == nil {
			check, err := knownhosts.New(file)
			if err != nil {
				return err
			}
			err = check(hostname, remote, key)
			var keyErr *knownhosts.KeyError
			if err == nil {
				return nil
			}
			if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
				*changed = true
				return err
			}
		}
		if err := os.MkdirAll(p.sshDir(), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n")
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		return err
	}
}

// dial is dialTailnet; tests replace it.
var dial = dialTailnet

// dialTailnet opens a TCP stream to host:port through the cloud computer's
// userspace tailscaled (`tailscale nc`), the same path ssh's ProxyCommand uses.
func dialTailnet(ctx context.Context, p paths, host string, port int) (net.Conn, error) {
	command := exec.CommandContext(ctx, p.tailscale(), "--socket="+p.socket(), "nc", host, strconv.Itoa(port))
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuffer{}
	command.Stderr = stderr
	command.WaitDelay = time.Second
	if err = command.Start(); err != nil {
		return nil, err
	}
	return &processConn{command: command, reader: stdout, writer: stdin, stderr: stderr, remote: &net.TCPAddr{IP: net.ParseIP(host), Port: port}}, nil
}

// processConn is a net.Conn over a child process's stdin and stdout.
type processConn struct {
	command *exec.Cmd
	reader  io.ReadCloser
	writer  io.WriteCloser
	stderr  *lockedBuffer
	remote  *net.TCPAddr
}

// dialError is what the dialing process printed, once it has exited.
func dialError(conn net.Conn) string {
	if c, ok := conn.(*processConn); ok {
		return c.stderr.String()
	}
	return ""
}

// lockedBuffer keeps the last few KiB a process writes to stderr.
type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 4096 {
		b.data = b.data[len(b.data)-4096:]
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func (c *processConn) Read(b []byte) (int, error)  { return c.reader.Read(b) }
func (c *processConn) Write(b []byte) (int, error) { return c.writer.Write(b) }
func (c *processConn) Close() error {
	c.writer.Close()
	if c.command.Process != nil {
		c.command.Process.Kill()
	}
	c.command.Wait()
	return nil
}
func (c *processConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *processConn) RemoteAddr() net.Addr             { return c.remote }
func (c *processConn) SetDeadline(time.Time) error      { return nil }
func (c *processConn) SetReadDeadline(time.Time) error  { return nil }
func (c *processConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "tailnet" }
func (pipeAddr) String() string  { return "tailnet" }
