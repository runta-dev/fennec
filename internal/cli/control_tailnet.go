package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/cloudops"
	"fennec/internal/control"
	"fennec/internal/tailnet"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Tailscale on the cloud computer (FEN-104). The panel's Settings › Tailscale
// page and Computers tab call these; each forwards one tailnet_* operation to
// the cloud computer over the resident channel.
func (s *daemonServices) tailnetHandlers() map[string]control.Handler {
	return map[string]control.Handler{
		"tailnet.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				Probe bool `json:"probe"`
			}
			json.Unmarshal(raw, &request)
			return s.tailnetStatus(ctx, request.Probe)
		},
		"tailnet.login": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_login"}, 30*time.Second)
		},
		"tailnet.logout": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_logout"}, 30*time.Second)
		},
		"tailnet.add": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request tailnet.Request
			if json.Unmarshal(raw, &request) != nil || request.Validate(true) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Choose a computer and a user name"}
			}
			return s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_add", Tailnet: &tailnet.Request{ID: request.ID, User: request.User}}, 28*time.Second)
		},
		"tailnet.remove": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request tailnet.Request
			if json.Unmarshal(raw, &request) != nil || request.Validate(false) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Choose a computer"}
			}
			return s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_remove", Tailnet: &tailnet.Request{ID: request.ID}}, 15*time.Second)
		},
	}
}

func (s *daemonServices) tailnetCall(ctx context.Context, operation cloudops.Operation, timeout time.Duration) (json.RawMessage, error) {
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Runta first"}
	}
	s.mu.Lock()
	channel, current := s.channel, s.epoch
	s.mu.Unlock()
	if channel == nil || current != epoch {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is connecting", Retryable: true}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is connecting", Retryable: true}
	}
	if !transport.Supports("tailnet") {
		return nil, &control.Fault{Code: "unsupported", Message: "Cloud computer is updating", Retryable: true}
	}
	return transport.Operate(ctx, operation)
}

// tailnetAutoAdd remembers when this Mac last tried to add itself, so a Mac
// without Remote Login isn't retried on every poll.
var tailnetAutoAdd struct {
	sync.Mutex
	tried map[string]time.Time
}

// tailnetStatus is the cloud computer's tailnet plus what only the Mac
// knows: which device is this Mac and the local user name. A Mac running
// Fennec adds itself: it authorizes the cloud computer's key and asks the
// cloud computer to sign in, so the user runs no command there.
func (s *daemonServices) tailnetStatus(ctx context.Context, probe bool) (any, error) {
	raw, err := s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_status", Tailnet: &tailnet.Request{Probe: probe}}, 25*time.Second)
	if err != nil {
		return nil, err
	}
	var status map[string]any
	if err = json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	name := localUser()
	status["local_user"] = name
	var view tailnet.Status
	if json.Unmarshal(raw, &view) != nil || view.State != "running" {
		return status, nil
	}
	local := localTailnetIPs()
	index := -1
	for i, device := range view.Devices {
		for _, ip := range device.IPs {
			if local[ip] {
				index = i
			}
		}
	}
	if index < 0 {
		return status, nil
	}
	device := view.Devices[index]
	status["this_device"] = device.ID
	if device.Computer != nil && device.Computer.Error == "" || view.PublicKey == "" || name == "" {
		return status, nil
	}
	tailnetAutoAdd.Lock()
	if tailnetAutoAdd.tried == nil {
		tailnetAutoAdd.tried = map[string]time.Time{}
	}
	recent := time.Since(tailnetAutoAdd.tried[device.ID]) < time.Minute
	if !recent {
		tailnetAutoAdd.tried[device.ID] = time.Now()
	}
	tailnetAutoAdd.Unlock()
	if recent {
		return status, nil
	}
	if err = authorizeTailnetKey(s.home, view.PublicKey); err != nil {
		status["this_device_error"] = "authorize_failed"
		return status, nil
	}
	added, err := s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_add", Tailnet: &tailnet.Request{ID: device.ID, User: name}}, 25*time.Second)
	var result tailnet.Device
	if err != nil || json.Unmarshal(added, &result) != nil || result.Computer == nil {
		return status, nil
	}
	if devices, ok := status["devices"].([]any); ok && index < len(devices) {
		var replacement any
		json.Unmarshal(added, &replacement)
		devices[index] = replacement
	}
	return status, nil
}

func localUser() string {
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	return os.Getenv("USER")
}

// localTailnetIPs are this computer's Tailscale addresses (100.64.0.0/10 and
// fd7a:115c:a1e0::/48), whichever Tailscale client owns the interface.
func localTailnetIPs() map[string]bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	_, ula, _ := net.ParseCIDR("fd7a:115c:a1e0::/48")
	found := map[string]bool{}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return found
	}
	for _, address := range addresses {
		prefix, ok := address.(*net.IPNet)
		if ok && (cgnat.Contains(prefix.IP) || ula.Contains(prefix.IP)) {
			found[prefix.IP.String()] = true
		}
	}
	return found
}

// authorizeTailnetKey adds the cloud computer's tailnet key to this Mac's
// authorized_keys (restricted to tailnet addresses), once.
func authorizeTailnetKey(home, key string) error {
	line := tailnet.AuthorizeLine(key)
	fields := strings.Fields(key)
	if line == "" || len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") {
		return os.ErrInvalid
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "authorized_keys")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.Contains(existing, fields[1]) {
			return nil
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	_, err = file.WriteString(prefix + line + "\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
