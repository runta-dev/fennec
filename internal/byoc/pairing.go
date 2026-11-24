// Package byoc pairs Fennec with a self-hosted ("bring your own cloud")
// cloud computer. It uses no Runta service: the computer is reached directly
// over SSH, trust comes from a host key pinned at pairing time, and the
// computer's identity is a machine ID stored on its persistent home volume.
package byoc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// DefaultUser is the unprivileged account in the published cloud image.
	DefaultUser = "fennec"
	// pairingVersion is the on-disk pairing record format.
	pairingVersion = 1
	maxRecordBytes = 16 << 10
)

// ErrNotPaired means no self-hosted cloud computer is paired on this Mac.
var ErrNotPaired = errors.New("no self-hosted cloud computer is paired")

var (
	machinePattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	userPattern    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

// ValidMachineID reports whether id is a self-hosted machine identity: a
// lowercase UUID, the same shape as a Runta runtime ID so that every session,
// attach and sync path can carry it unchanged as the runtime ID.
func ValidMachineID(id string) bool { return machinePattern.MatchString(id) }

// Pairing is the durable record of the paired computer. HostKey is the
// computer's SSH host public key in authorized_keys format, obtained over a
// channel the user verified; it is never replaced by a later connection.
type Pairing struct {
	Version   int       `json:"version"`
	MachineID string    `json:"machine_id"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	User      string    `json:"user"`
	HostKey   string    `json:"host_key"`
	PairedAt  time.Time `json:"paired_at"`
}

// Validate rejects records that could not be used safely as an SSH target.
func (p Pairing) Validate() error {
	if p.Version != pairingVersion {
		return fmt.Errorf("unsupported pairing record version %d", p.Version)
	}
	if !ValidMachineID(p.MachineID) {
		return errors.New("invalid cloud computer identity")
	}
	if err := validHost(p.Host); err != nil {
		return err
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("invalid SSH port")
	}
	if !userPattern.MatchString(p.User) {
		return errors.New("invalid SSH user")
	}
	if _, err := p.PublicHostKey(); err != nil {
		return err
	}
	return nil
}

func validHost(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		return nil
	}
	if !hostPattern.MatchString(host) || strings.Contains(host, "..") {
		return errors.New("invalid SSH host")
	}
	return nil
}

// PublicHostKey parses the pinned host key.
func (p Pairing) PublicHostKey() (ssh.PublicKey, error) {
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(p.HostKey))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid pinned host key")
	}
	return key, nil
}

// Address is the host:port the computer's SSH service listens on.
func (p Pairing) Address() string { return net.JoinHostPort(p.Host, strconv.Itoa(p.Port)) }

// KnownHostsLine is the OpenSSH known_hosts entry for the pinned key. A
// non-default port uses OpenSSH's bracketed host form.
func (p Pairing) KnownHostsLine() (string, error) {
	key, err := p.PublicHostKey()
	if err != nil {
		return "", err
	}
	pattern := p.Host
	if p.Port != 22 {
		pattern = "[" + p.Host + "]:" + strconv.Itoa(p.Port)
	}
	return pattern + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + "\n", nil
}

// Dir holds the pairing record, the device key (runtime_key) and the pinned
// known_hosts, with the same file names as the Runta cloud directory so the
// OpenSSH launch paths can use either directory unchanged.
func Dir(home string) string { return filepath.Join(home, ".config", "fennec", "byoc") }

func recordPath(home string) string { return filepath.Join(Dir(home), "pairing.json") }

// Load returns the paired computer, or ErrNotPaired.
func Load(home string) (Pairing, error) {
	data, err := os.ReadFile(recordPath(home))
	if os.IsNotExist(err) {
		return Pairing{}, ErrNotPaired
	}
	if err != nil {
		return Pairing{}, err
	}
	if len(data) > maxRecordBytes {
		return Pairing{}, errors.New("pairing record is too large")
	}
	var p Pairing
	if err := json.Unmarshal(data, &p); err != nil {
		return Pairing{}, errors.New("pairing record is unreadable")
	}
	if err := p.Validate(); err != nil {
		return Pairing{}, err
	}
	return p, nil
}

// Save records p as the paired computer and writes its known_hosts entry.
// known_hosts is written first: a record is only visible once the pin it
// depends on is in place.
func Save(home string, p Pairing) error {
	if err := p.Validate(); err != nil {
		return err
	}
	line, err := p.KnownHostsLine()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "known_hosts"), []byte(line)); err != nil {
		return err
	}
	return writePrivate(recordPath(home), append(data, '\n'))
}

// Remove forgets the paired computer. The device key is removed with it, so
// pairing again requires authorizing a fresh key on the computer.
func Remove(home string) error {
	if err := os.RemoveAll(Dir(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writePrivate(path string, data []byte) error {
	staged, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := staged.Name()
	defer os.Remove(name)
	if err := staged.Chmod(0600); err != nil {
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
