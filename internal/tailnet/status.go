package tailnet

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// backend is the part of `tailscale status --json` Fennec reads.
type backend struct {
	BackendState   string
	AuthURL        string
	Health         []string
	Self           *node
	Peer           map[string]*node
	User           map[string]struct{ LoginName, DisplayName string }
	CurrentTailnet *struct{ Name string }
}

type node struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	TailscaleIPs []string
	Online       bool
	LastSeen     time.Time
	UserID       int64
}

func (n *node) device() Device {
	d := Device{ID: n.ID, Name: n.HostName, DNSName: strings.TrimSuffix(n.DNSName, "."), OS: n.OS, Online: n.Online, IPs: n.TailscaleIPs}
	if short, _, ok := strings.Cut(d.DNSName, "."); ok && short != "" {
		// MagicDNS names are unique in the tailnet; host names aren't.
		d.Name = short
	}
	if !n.LastSeen.IsZero() && n.LastSeen.Year() > 1 {
		d.LastSeen = n.LastSeen.UTC().Format(time.RFC3339)
	}
	return d
}

// ReadStatus reports Tailscale on the cloud computer and the tailnet's
// devices. With probe, added computers that are online are checked over SSH
// and their metrics refreshed.
func ReadStatus(ctx context.Context, home string, probe bool) Status {
	p := paths{home}
	status := Status{State: "not_installed", Installed: p.installed(), Devices: []Device{}}
	current := readJob(p)
	if status.Installed {
		status.Version = installedVersion(p)
		status.State = "stopped"
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := cli(readCtx, p, "status", "--json")
		cancel()
		var b backend
		if err == nil && json.Unmarshal(raw, &b) == nil {
			applyBackend(&status, b)
		}
	}
	if current.active() && status.State != "running" {
		switch current.Stage {
		case "installing":
			status.State = "installing"
		case "starting":
			status.State = "starting"
		default:
			// Until the login URL arrives, tailscaled already says NeedsLogin.
			if status.State == "stopped" || status.State == "needs_login" && status.AuthURL == "" && status.Error == "" {
				status.State = "starting"
			}
		}
	}
	if current.Stage == "failed" && current.Error != "" && status.State != "running" && status.Error == "" {
		status.Error, status.Detail = current.Error, current.Detail
		if status.State == "not_installed" || status.State == "stopped" {
			status.State = "failed"
		}
	}
	if status.State != "running" {
		return status
	}
	if key, err := ensureKey(p); err == nil {
		status.PublicKey = key
		status.Commands = AuthorizeCommands(key)
	}
	attachComputers(ctx, p, &status, probe)
	return status
}

func applyBackend(status *Status, b backend) {
	switch b.BackendState {
	case "Running":
		status.State = "running"
	case "NeedsLogin":
		status.State = "needs_login"
	case "NeedsMachineAuth":
		status.State = "needs_approval"
	case "Starting":
		status.State = "starting"
	default:
		status.State = "stopped"
	}
	status.AuthURL = b.AuthURL
	if status.State != "running" {
		for _, message := range b.Health {
			if code := classifyLoginError(message); code != "" {
				status.Error, status.Detail = code, message
				break
			}
		}
	}
	if b.CurrentTailnet != nil {
		status.Tailnet = b.CurrentTailnet.Name
	}
	if b.Self != nil && status.State == "running" {
		self := b.Self.device()
		self.Online = true
		status.Self = &self
		for id, user := range b.User {
			if id == jsonID(b.Self.UserID) {
				status.User = user.LoginName
			}
		}
	}
	for _, peer := range b.Peer {
		if peer == nil || peer.ID == "" {
			continue
		}
		status.Devices = append(status.Devices, peer.device())
	}
	sort.Slice(status.Devices, func(i, j int) bool {
		a, c := status.Devices[i], status.Devices[j]
		if a.Online != c.Online {
			return a.Online
		}
		return strings.ToLower(a.Name) < strings.ToLower(c.Name)
	})
}

func jsonID(id int64) string {
	data, _ := json.Marshal(id)
	return string(data)
}

// classifyLoginError names a failed registration. The egress gateway of a
// Runta cloud computer terminates TLS and only passes WebSocket upgrades, so
// Tailscale's control protocol upgrade comes back as a bare 400 (FEN-104).
func classifyLoginError(message string) string {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "login error") && !strings.Contains(lower, "register request") {
		return ""
	}
	if strings.Contains(lower, "unexpected http response: 400") || strings.Contains(lower, "all connection attempts failed") {
		return ErrNetworkBlocked
	}
	return ErrLoginFailed
}
