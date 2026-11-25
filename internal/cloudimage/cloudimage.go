// Package cloudimage prepares the Fennec self-hosted cloud image at container
// start: a persistent SSH host key, a persistent machine identity, and the
// authorized device keys, all kept on the home volume so they survive
// container replacement. It depends on nothing but the SSH library, so the
// image's entry point can be built from it alone.
package cloudimage

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Layout names every path the image uses. Defaults are in DefaultLayout;
// tests substitute a temporary root.
type Layout struct {
	// Home is the SSH user's home and the persistent volume.
	Home string
	// AuthorizedKeys is the file sshd reads (AuthorizedKeysFile). It is
	// regenerated from the persistent list at every start.
	AuthorizedKeys string
	// UID and GID own Home. Ownership changes are skipped when Chown is nil.
	UID, GID int
	Chown    func(path string, uid, gid int) error
	// Seed holds tools built into the image (Node.js) that Fennec would
	// otherwise download on first connection. Empty disables seeding.
	Seed string
}

// DefaultLayout is the published image's layout.
func DefaultLayout() Layout {
	return Layout{Home: "/home/fennec", AuthorizedKeys: "/etc/ssh/fennec_authorized_keys", UID: 1000, GID: 1000, Chown: os.Lchown, Seed: "/opt/fennec/seed"}
}

// State is root-owned (0700) inside the home volume: the SSH user cannot read
// the host private key or change which devices are authorized.
func (l Layout) State() string { return filepath.Join(l.Home, ".fennec-host") }

// HostKey is the persistent SSH host private key.
func (l Layout) HostKey() string { return filepath.Join(l.State(), "ssh_host_ed25519_key") }

func (l Layout) devices() string    { return filepath.Join(l.State(), "authorized_keys") }
func (l Layout) identity() string   { return filepath.Join(l.State(), "identity.json") }
func (l Layout) publicDir() string  { return filepath.Join(l.Home, ".config", "fennec-cloud") }
func (l Layout) publicPath() string { return filepath.Join(l.publicDir(), "identity.json") }

// Report is what the container prints at start for the user to pair with.
type Report struct {
	MachineID   string
	Fingerprint string
	Devices     int
}

// Init prepares the volume and publishes the files sshd and Fennec read.
// It is idempotent: an existing host key and identity are always kept.
// initialKey, when set, is authorized (the FENNEC_AUTHORIZED_KEY variable).
func Init(l Layout, initialKey string) (Report, error) {
	if err := os.MkdirAll(l.Home, 0755); err != nil {
		return Report{}, err
	}
	// A bind-mounted or fresh volume may be root-owned; the user must own
	// its home for sshd's StrictModes and for agents to write to it.
	if err := l.chown(l.Home); err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(l.State(), 0700); err != nil {
		return Report{}, err
	}
	if err := os.Chmod(l.State(), 0700); err != nil {
		return Report{}, err
	}
	if err := seedTools(l); err != nil {
		return Report{}, err
	}
	fingerprint, err := ensureHostKey(l)
	if err != nil {
		return Report{}, err
	}
	machine, err := ensureIdentity(l)
	if err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(initialKey) != "" {
		if _, err := addDevice(l, initialKey); err != nil {
			return Report{}, fmt.Errorf("FENNEC_AUTHORIZED_KEY: %w", err)
		}
	}
	devices, err := Devices(l)
	if err != nil {
		return Report{}, err
	}
	if err := publish(l, devices); err != nil {
		return Report{}, err
	}
	return Report{MachineID: machine, Fingerprint: fingerprint, Devices: len(devices)}, nil
}

func (l Layout) chown(path string) error {
	if l.Chown == nil {
		return nil
	}
	return l.Chown(path, l.UID, l.GID)
}

func ensureHostKey(l Layout) (string, error) {
	data, err := os.ReadFile(l.HostKey())
	if os.IsNotExist(err) {
		_, private, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return "", genErr
		}
		block, marshalErr := ssh.MarshalPrivateKey(private, "fennec-cloud")
		if marshalErr != nil {
			return "", marshalErr
		}
		data = pem.EncodeToMemory(block)
		if err := writeFile(l.HostKey(), data, 0600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return "", fmt.Errorf("host key is unreadable: %w", err)
	}
	public := ssh.MarshalAuthorizedKey(signer.PublicKey())
	if err := writeFile(l.HostKey()+".pub", public, 0644); err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type identityDocument struct {
	MachineID string `json:"machine_id"`
}

func ensureIdentity(l Layout) (string, error) {
	var identity identityDocument
	data, err := os.ReadFile(l.identity())
	switch {
	case os.IsNotExist(err):
		id, genErr := newUUID()
		if genErr != nil {
			return "", genErr
		}
		identity.MachineID = id
		encoded, _ := json.Marshal(identity)
		if err := writeFile(l.identity(), append(encoded, '\n'), 0600); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if json.Unmarshal(data, &identity) != nil || !uuidPattern.MatchString(identity.MachineID) {
			return "", errors.New("machine identity is unreadable; refusing to replace it")
		}
	}
	return identity.MachineID, nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// publish writes the identity Fennec reads and the key file sshd reads. Both
// are root-owned and world-readable, so the SSH user can read but not change
// them.
func publish(l Layout, devices []string) error {
	if err := os.MkdirAll(l.publicDir(), 0755); err != nil {
		return err
	}
	if err := os.Chmod(l.publicDir(), 0755); err != nil {
		return err
	}
	// ~/.config itself belongs to the user, like any other dotfile directory.
	if err := l.chown(filepath.Dir(l.publicDir())); err != nil {
		return err
	}
	identity, err := os.ReadFile(l.identity())
	if err != nil {
		return err
	}
	if err := writeFile(l.publicPath(), identity, 0644); err != nil {
		return err
	}
	var keys bytes.Buffer
	for _, key := range devices {
		keys.WriteString(key + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(l.AuthorizedKeys), 0755); err != nil {
		return err
	}
	return writeFile(l.AuthorizedKeys, keys.Bytes(), 0644)
}

// Devices lists the authorized device keys in authorized_keys format.
func Devices(l Layout) ([]string, error) {
	data, err := os.ReadFile(l.devices())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			keys = append(keys, line)
		}
	}
	return keys, nil
}

// canonicalKey accepts exactly one public key without authorized_keys
// options, so an authorization can never carry a forced command, an
// environment, or forwarding permissions.
func canonicalKey(raw string) (ssh.PublicKey, string, error) {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(raw)))
	if err != nil {
		return nil, "", errors.New("not an SSH public key")
	}
	if len(options) != 0 {
		return nil, "", errors.New("key options are not accepted")
	}
	if len(strings.TrimSpace(string(rest))) != 0 {
		return nil, "", errors.New("exactly one key is required")
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment = strings.Join(strings.Fields(comment), " "); comment != "" {
		line += " " + comment
	}
	return key, line, nil
}

func addDevice(l Layout, raw string) (bool, error) {
	key, line, err := canonicalKey(raw)
	if err != nil {
		return false, err
	}
	devices, err := Devices(l)
	if err != nil {
		return false, err
	}
	for _, existing := range devices {
		if parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(existing)); err == nil && bytes.Equal(parsed.Marshal(), key.Marshal()) {
			return false, nil
		}
	}
	return true, writeDevices(l, append(devices, line))
}

func writeDevices(l Layout, devices []string) error {
	var data bytes.Buffer
	for _, key := range devices {
		data.WriteString(key + "\n")
	}
	return writeFile(l.devices(), data.Bytes(), 0600)
}

// Authorize adds a device key and republishes the key file sshd reads.
func Authorize(l Layout, raw string) (bool, error) {
	added, err := addDevice(l, raw)
	if err != nil {
		return false, err
	}
	devices, err := Devices(l)
	if err != nil {
		return false, err
	}
	return added, publish(l, devices)
}

// Revoke removes the device whose public key or SHA256 fingerprint matches.
func Revoke(l Layout, target string) (bool, error) {
	target = strings.TrimSpace(target)
	var match func(ssh.PublicKey) bool
	if strings.HasPrefix(target, "SHA256:") {
		match = func(key ssh.PublicKey) bool { return ssh.FingerprintSHA256(key) == target }
	} else {
		wanted, _, err := canonicalKey(target)
		if err != nil {
			return false, err
		}
		match = func(key ssh.PublicKey) bool { return bytes.Equal(key.Marshal(), wanted.Marshal()) }
	}
	devices, err := Devices(l)
	if err != nil {
		return false, err
	}
	kept := devices[:0]
	removed := false
	for _, line := range devices {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err == nil && match(key) {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return false, nil
	}
	if err := writeDevices(l, kept); err != nil {
		return false, err
	}
	return true, publish(l, kept)
}

// Fingerprint returns the host key fingerprint without changing anything.
func Fingerprint(l Layout) (string, error) {
	data, err := os.ReadFile(l.HostKey() + ".pub")
	if err != nil {
		return "", err
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(key), nil
}

// MachineID returns the persistent machine identity without changing anything.
func MachineID(l Layout) (string, error) {
	data, err := os.ReadFile(l.identity())
	if err != nil {
		return "", err
	}
	var identity identityDocument
	if json.Unmarshal(data, &identity) != nil || !uuidPattern.MatchString(identity.MachineID) {
		return "", errors.New("machine identity is unreadable")
	}
	return identity.MachineID, nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	staged, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := staged.Name()
	defer os.Remove(name)
	if err := staged.Chmod(mode); err != nil {
		staged.Close()
		return err
	}
	if _, err := staged.Write(data); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
