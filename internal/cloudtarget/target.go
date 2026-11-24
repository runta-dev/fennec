// Package cloudtarget resolves the SSH destination of the active cloud
// computer for every SSH path: the Go client, OpenSSH child processes, and
// generated ssh configs. A Runta computer is reached through the Runta SSH
// gateway, which routes by user name (the runtime ID). A paired self-hosted
// computer is reached directly at its pinned host, port and user.
package cloudtarget

import (
	"errors"
	"fennec/internal/byoc"
	"fennec/internal/runta"
	"net"
	"path/filepath"
	"strconv"
)

// Target is one cloud computer's SSH destination. Dir holds the device key
// (runtime_key) and the pinned host key (known_hosts).
type Target struct {
	Dir  string
	Host string
	Port int
	// User is the fixed SSH login of a self-hosted computer. It is empty for
	// the Runta gateway, whose login is the runtime ID.
	User string
}

// RuntaDir is the Runta cloud computer's SSH identity directory.
func RuntaDir(home string) string { return filepath.Join(home, ".config", "fennec", "cloud") }

// Runta is the Runta gateway target for home's active Runta environment.
func Runta(home string) Target {
	return Target{Dir: RuntaDir(home), Host: runta.ActiveSSHHost(home), Port: 22}
}

// SelfHosted is the target of a paired self-hosted computer.
func SelfHosted(home string, p byoc.Pairing) Target {
	return Target{Dir: byoc.Dir(home), Host: p.Host, Port: p.Port, User: p.User}
}

// Active returns the target of the cloud computer this Mac uses: the paired
// self-hosted computer when one is paired, otherwise the Runta computer. An
// unreadable pairing record is an error, never a silent switch to Runta.
func Active(home string) (Target, error) {
	p, err := byoc.Load(home)
	if errors.Is(err, byoc.ErrNotPaired) {
		return Runta(home), nil
	}
	if err != nil {
		return Target{}, err
	}
	return SelfHosted(home, p), nil
}

// ForDir resolves the target whose identity directory is directory. Paths
// that only validate arguments pass directories outside a Fennec home; those
// keep the Runta gateway of the current user's environment.
func ForDir(directory string) (Target, error) {
	clean := filepath.Clean(directory)
	parent := filepath.Dir(clean)
	if filepath.Base(parent) == "fennec" && filepath.Base(filepath.Dir(parent)) == ".config" {
		home := filepath.Dir(filepath.Dir(parent))
		switch filepath.Base(clean) {
		case "cloud":
			return Runta(home), nil
		case "byoc":
			p, err := byoc.Load(home)
			if err != nil {
				return Target{}, err
			}
			return SelfHosted(home, p), nil
		}
	}
	return Target{Dir: clean, Host: runta.ActiveSSHHost(""), Port: 22}, nil
}

// Home returns the Fennec home owning the target's identity directory, or ""
// when the directory is not inside a Fennec home.
func (t Target) Home() string {
	parent := filepath.Dir(filepath.Clean(t.Dir))
	if filepath.Base(parent) == "fennec" && filepath.Base(filepath.Dir(parent)) == ".config" {
		return filepath.Dir(filepath.Dir(parent))
	}
	return ""
}

// SSHPort is the TCP port to dial (22 unless the target sets one).
func (t Target) SSHPort() int { return t.port() }

func (t Target) port() int {
	if t.Port == 0 {
		return 22
	}
	return t.Port
}

// Login is the SSH user for runtimeID.
func (t Target) Login(runtimeID string) string {
	if t.User != "" {
		return t.User
	}
	return runtimeID
}

// Destination is the OpenSSH user@host destination for runtimeID.
func (t Target) Destination(runtimeID string) string { return t.Login(runtimeID) + "@" + t.Host }

// Address is the host:port to dial.
func (t Target) Address() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.port())) }

// Key is the device private key path.
func (t Target) Key() string { return filepath.Join(t.Dir, "runtime_key") }

// KnownHosts is the pinned known_hosts path.
func (t Target) KnownHosts() string { return filepath.Join(t.Dir, "known_hosts") }

// OpenSSHArgs returns the identity, pinned host key and port options followed
// by the destination. Callers append any remote command after them.
func (t Target) OpenSSHArgs(runtimeID string) []string {
	args := []string{"-o", "UserKnownHostsFile=" + t.KnownHosts(), "-i", t.Key()}
	if t.port() != 22 {
		args = append(args, "-p", strconv.Itoa(t.port()))
	}
	return append(args, t.Destination(runtimeID))
}

// AllowsDial reports whether host:port is the SSH endpoint of home's Runta
// gateway or of its paired self-hosted computer. The fd-passing dial helper
// refuses every other destination.
func AllowsDial(home, host, port string) bool {
	if host == "" {
		return false
	}
	if host == runta.ActiveSSHHost(home) && port == "22" {
		return true
	}
	p, err := byoc.Load(home)
	return err == nil && host == p.Host && port == strconv.Itoa(p.Port)
}

// TakeoverPreferencesPath is where the selected agents, project roots and
// runtime hint for the active cloud computer are saved. A paired self-hosted
// computer keeps its own file, so switching between it and a Runta computer
// never overwrites the other's selection.
func TakeoverPreferencesPath(home string) string {
	if _, err := byoc.Load(home); err == nil {
		return filepath.Join(byoc.Dir(home), "takeover-preferences.json")
	}
	return runta.TakeoverPreferencesPath(home)
}
