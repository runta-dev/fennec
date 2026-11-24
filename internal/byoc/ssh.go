package byoc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// IdentityPath is where the cloud image keeps the machine identity, relative
// to the SSH user's home. It lives on the persistent home volume, so the
// identity survives container replacement and changes only with the volume.
const IdentityPath = ".config/fennec-cloud/identity.json"

const (
	dialTimeout      = 20 * time.Second
	maxIdentityBytes = 4 << 10
)

// ErrIdentityMismatch means the computer answering at the paired address is
// not the paired machine (for example its volume was replaced). It is never
// treated as a transient failure and never re-pins anything.
var ErrIdentityMismatch = errors.New("the cloud computer at this address is not the paired machine")

// Endpoint is where an unpaired computer is reached during pairing.
type Endpoint struct {
	Host string
	Port int
	User string
}

func (e Endpoint) validate() error {
	if err := validHost(e.Host); err != nil {
		return err
	}
	if e.Port < 1 || e.Port > 65535 {
		return errors.New("invalid SSH port")
	}
	if !userPattern.MatchString(e.User) {
		return errors.New("invalid SSH user")
	}
	return nil
}

// DeviceKey loads this Mac's SSH key for self-hosted computers, creating it on
// first use. Its public half is what the user authorizes on the computer.
func DeviceKey(home string) (ssh.Signer, error) {
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "runtime_key")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, private, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, genErr
		}
		block, marshalErr := ssh.MarshalPrivateKey(private, "Fennec self-hosted cloud computer")
		if marshalErr != nil {
			return nil, marshalErr
		}
		data = pem.EncodeToMemory(block)
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(createErr, os.ErrExist) {
			// Another process created it first; use that key.
			data, err = os.ReadFile(path)
		} else if createErr != nil {
			return nil, createErr
		} else {
			_, err = file.Write(data)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

// AuthorizedKey is the device public key in authorized_keys format.
func AuthorizedKey(signer ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// matchesExpectation compares a presented host key with what the user
// verified: either an SHA256 fingerprint ("SHA256:...") or the full public
// key in authorized_keys format. Comparison is constant-time.
func matchesExpectation(key ssh.PublicKey, expected string) bool {
	expected = strings.TrimSpace(expected)
	if strings.HasPrefix(expected, "SHA256:") {
		got := ssh.FingerprintSHA256(key)
		return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
	}
	pinned, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(expected))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return false
	}
	return subtle.ConstantTimeCompare(pinned.Marshal(), key.Marshal()) == 1
}

// Discover connects to an unpaired computer, accepting its host key only if
// it matches expected (a fingerprint or public key the user obtained through
// a trusted channel), and reads the machine identity. It returns the pairing
// record to save and the verified connection; it saves nothing itself.
func Discover(ctx context.Context, home string, endpoint Endpoint, expected string) (Pairing, *ssh.Client, error) {
	if err := endpoint.validate(); err != nil {
		return Pairing{}, nil, err
	}
	if strings.TrimSpace(expected) == "" {
		return Pairing{}, nil, errors.New("a verified host key fingerprint is required")
	}
	signer, err := DeviceKey(home)
	if err != nil {
		return Pairing{}, nil, err
	}
	var presented ssh.PublicKey
	config := &ssh.ClientConfig{
		User: endpoint.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !matchesExpectation(key, expected) {
				return fmt.Errorf("host key %s does not match the verified fingerprint", ssh.FingerprintSHA256(key))
			}
			presented = key
			return nil
		},
		Timeout: dialTimeout,
	}
	address := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	client, err := dial(ctx, address, config)
	if err != nil {
		return Pairing{}, nil, err
	}
	machine, err := ReadIdentity(ctx, client)
	if err != nil {
		client.Close()
		return Pairing{}, nil, err
	}
	p := Pairing{
		Version:   pairingVersion,
		MachineID: machine,
		Host:      endpoint.Host,
		Port:      endpoint.Port,
		User:      endpoint.User,
		HostKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(presented))),
		PairedAt:  time.Now().UTC(),
	}
	if err := p.Validate(); err != nil {
		client.Close()
		return Pairing{}, nil, err
	}
	return p, client, nil
}

// ClientConfig authenticates with the device key and accepts only the pinned
// host key. There is no host-key bypass.
func ClientConfig(home string, p Pairing) (*ssh.ClientConfig, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	hostKey, err := p.PublicHostKey()
	if err != nil {
		return nil, err
	}
	signer, err := DeviceKey(home)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            p.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         dialTimeout,
	}, nil
}

// Connect opens a connection to the paired computer and verifies that it is
// still the paired machine before returning it.
func Connect(ctx context.Context, home string, p Pairing) (*ssh.Client, error) {
	config, err := ClientConfig(home, p)
	if err != nil {
		return nil, err
	}
	client, err := dial(ctx, p.Address(), config)
	if err != nil {
		return nil, err
	}
	machine, err := ReadIdentity(ctx, client)
	if err == nil && machine != p.MachineID {
		err = ErrIdentityMismatch
	}
	if err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// ReadIdentity returns the machine ID the computer reports.
func ReadIdentity(ctx context.Context, client *ssh.Client) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := session.Start(`cat -- "$HOME/` + IdentityPath + `"`); err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxIdentityBytes+1))
	waitErr := session.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if readErr != nil {
		return "", readErr
	}
	if waitErr != nil || len(data) == 0 {
		return "", errors.New("the cloud computer has no Fennec identity; is it running the Fennec cloud image?")
	}
	if len(data) > maxIdentityBytes {
		return "", errors.New("cloud computer identity is too large")
	}
	var identity struct {
		MachineID string `json:"machine_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&identity); err != nil || !ValidMachineID(identity.MachineID) {
		return "", errors.New("cloud computer identity is invalid")
	}
	return identity.MachineID, nil
}

func dial(ctx context.Context, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	raw, err := (&net.Dialer{KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(dialTimeout))
	conn, channels, requests, err := ssh.NewClientConn(raw, address, config)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(conn, channels, requests), nil
}
