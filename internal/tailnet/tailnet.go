// Package tailnet runs Tailscale on the cloud computer and lets it reach the
// user's other computers over their tailnet with plain SSH (FEN-104).
//
// The cloud computer is the tailnet node: Fennec installs the static Tailscale
// build under ~/.local/fennec/tailscale, runs tailscaled in userspace
// networking mode (it never touches the cloud computer's own routes, DNS or
// firewall, so the Mac relay can't be cut off), and signs in with
// `tailscale up`. Other computers are added by authorizing a dedicated
// ed25519 public key; the private key never leaves the cloud computer. SSH
// reaches peers through `tailscale nc`, and every added computer gets an alias
// in ~/.ssh/fennec/tailnet.conf so agents can `ssh <name>`.
package tailnet

import (
	"os"
	"path/filepath"
)

// Request is the tailnet part of a cloud operation.
type Request struct {
	// ID is the peer's Tailscale stable node ID.
	ID string `json:"id,omitempty"`
	// User is the account on that computer the cloud computer signs in as.
	User string `json:"user,omitempty"`
	// Probe asks Status to sign in to added computers for fresh metrics.
	Probe bool `json:"probe,omitempty"`
	// Port is the SSH port on that computer; 0 means 22.
	Port int `json:"port,omitempty"`
	// Password or PrivateKey (with Passphrase if it is encrypted) signs in
	// once to authorize the cloud computer's key (FEN-123). They are used
	// for that one sign-in only and never stored.
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// Status is what the cloud computer reports to the Mac.
type Status struct {
	// State: not_installed, installing, starting, needs_login, needs_approval,
	// running, stopped, failed.
	State     string   `json:"state"`
	Installed bool     `json:"installed"`
	Version   string   `json:"version,omitempty"`
	AuthURL   string   `json:"auth_url,omitempty"`
	Error     string   `json:"error,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	User      string   `json:"user,omitempty"`
	Tailnet   string   `json:"tailnet,omitempty"`
	Self      *Device  `json:"self,omitempty"`
	Devices   []Device `json:"devices"`
	PublicKey string   `json:"public_key,omitempty"`
	// Commands authorize PublicKey on a new computer: "posix" (macOS, Linux)
	// and "windows" (PowerShell).
	Commands map[string]string `json:"commands,omitempty"`
}

// Device is one node of the tailnet.
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	DNSName  string    `json:"dns_name,omitempty"`
	OS       string    `json:"os,omitempty"`
	Online   bool      `json:"online"`
	IPs      []string  `json:"ips,omitempty"`
	LastSeen string    `json:"last_seen,omitempty"`
	Computer *Computer `json:"computer,omitempty"`
}

// Computer is a device the cloud computer was added to.
type Computer struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Alias   string   `json:"alias"`
	Host    string   `json:"host"`
	User    string   `json:"user"`
	Port    int      `json:"port,omitempty"`
	AddedAt int64    `json:"added_at"`
	Checked int64    `json:"checked_at,omitempty"`
	Error   string   `json:"error,omitempty"`
	Metrics *Metrics `json:"metrics,omitempty"`
}

// Metrics is one sample read from an added computer over SSH.
type Metrics struct {
	Name       string  `json:"name,omitempty"`
	OSName     string  `json:"os_name,omitempty"`
	OSVersion  string  `json:"os_version,omitempty"`
	CPUCount   int     `json:"cpu_count,omitempty"`
	CPUPercent float64 `json:"cpu_percent"`
	MemTotal   uint64  `json:"memory_total_bytes,omitempty"`
	MemUsed    uint64  `json:"memory_used_bytes,omitempty"`
	DiskTotal  uint64  `json:"disk_total_bytes,omitempty"`
	DiskUsed   uint64  `json:"disk_used_bytes,omitempty"`
}

// Error codes in Status.Error and Computer.Error.
const (
	// ErrNetworkBlocked: the cloud computer's egress gateway rejects
	// Tailscale's control or relay connection.
	ErrNetworkBlocked = "network_blocked"
	ErrLoginFailed    = "login_failed"
	ErrInstallFailed  = "install_failed"
	ErrStartFailed    = "start_failed"
	// SSH outcomes for a computer.
	ErrAuthFailed    = "auth_failed" // key not authorized for that user
	ErrRefused       = "refused"     // no SSH server (Remote Login off)
	ErrUnreachable   = "unreachable" // offline or no route
	ErrHostKeyChange = "host_key_changed"
	ErrSSHFailed     = "ssh_failed"
	// ErrShieldsUp: the Mac's own Tailscale blocks incoming connections
	// ("Allow incoming connections" off), so SSH from the cloud computer
	// stalls in the banner exchange while pings still answer (FEN-156).
	ErrShieldsUp = "shields_up"
)

type paths struct{ home string }

func (p paths) bin() string        { return filepath.Join(p.home, ".local/fennec/tailscale") }
func (p paths) tailscale() string  { return filepath.Join(p.bin(), "tailscale") }
func (p paths) tailscaled() string { return filepath.Join(p.bin(), "tailscaled") }
func (p paths) state() string      { return filepath.Join(p.home, ".local/state/fennec/tailscale") }
func (p paths) socket() string     { return filepath.Join(p.state(), "tailscaled.sock") }
func (p paths) job() string        { return filepath.Join(p.state(), "job.json") }
func (p paths) daemonLog() string  { return filepath.Join(p.state(), "tailscaled.log") }
func (p paths) pidFile() string    { return filepath.Join(p.state(), "tailscaled.pid") }
func (p paths) upLog() string      { return filepath.Join(p.state(), "up.log") }
func (p paths) sshDir() string     { return filepath.Join(p.home, ".ssh/fennec") }
func (p paths) key() string        { return filepath.Join(p.sshDir(), "tailnet_ed25519") }
func (p paths) knownHosts() string { return filepath.Join(p.sshDir(), "tailnet_known_hosts") }
func (p paths) sshConfig() string  { return filepath.Join(p.sshDir(), "tailnet.conf") }
func (p paths) computers() string {
	return filepath.Join(p.home, ".config/fennec/tailnet-computers.json")
}

func (p paths) installed() bool {
	for _, file := range []string{p.tailscale(), p.tailscaled()} {
		if info, err := os.Stat(file); err != nil || info.Mode()&0111 == 0 {
			return false
		}
	}
	return true
}
