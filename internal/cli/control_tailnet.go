package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/cloudops"
	"fennec/internal/control"
	"fennec/internal/runta"
	"fennec/internal/tailnet"
	"log"
	"net"
	"os"
	"os/exec"
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
			s.ensureTailnetPassthrough(ctx)
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
			// A password or private key goes to the cloud computer for one
			// sign-in that authorizes its key there (FEN-123); never logged.
			add := tailnet.Request{ID: request.ID, User: request.User, Port: request.Port, Password: request.Password, PrivateKey: request.PrivateKey, Passphrase: request.Passphrase}
			return s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_add", Tailnet: &add}, 55*time.Second)
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

// ensureTailnetPassthrough lets Tailscale through the Runta egress gateway
// before sign-in: it adds Tailscale's control and DERP hosts to the cloud
// computer's TLS passthrough list (FEN-129). It is best effort. If the API
// refuses, sign-in still runs and the panel reports network_blocked as before.
func (s *daemonServices) ensureTailnetPassthrough(ctx context.Context) {
	_, principal, _, ok := s.account.Session()
	token, isRunta := principal.RuntaToken()
	if !ok || !isRunta {
		return
	}
	s.mu.Lock()
	runtimeID := s.config.RuntimeID
	s.mu.Unlock()
	if runtimeID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	changed, err := runta.NewClient(s.home, token).EnsureTLSPassthrough(ctx, runtimeID, runta.TailscaleTLSPassthroughHosts)
	switch {
	case err != nil:
		log.Printf("tailnet: TLS passthrough for Tailscale not set: %v", err)
	case changed:
		log.Printf("tailnet: added Tailscale hosts to the cloud computer's TLS passthrough")
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
// without Remote Login isn't retried on every poll, and the last failed
// attempt's device, which the status shows until an attempt succeeds.
var tailnetAutoAdd struct {
	sync.Mutex
	tried   map[string]time.Time
	running map[string]bool
	failed  map[string]json.RawMessage
}

// tailnetStatus is the cloud computer's tailnet plus what only the Mac
// knows: which device is this Mac and the local user name. A Mac running
// Fennec adds itself: it authorizes the cloud computer's key and asks the
// cloud computer to sign in, so the user runs no command there. The sign-in
// runs in the background: it waits for SSH, up to 20 s when this Mac isn't
// reachable, and the panel shows "Loading…" until status answers (FEN-147).
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
		tailnetAutoAdd.Lock()
		delete(tailnetAutoAdd.failed, device.ID)
		tailnetAutoAdd.Unlock()
		return status, nil
	}
	// With "Allow incoming connections" off, Tailscale on this Mac drops the
	// cloud computer's SSH: say so instead of a timeout, and don't retry.
	if localShieldsUp() {
		markThisDevice(status, index, tailnet.ErrShieldsUp)
		return status, nil
	}
	tailnetAutoAdd.Lock()
	if tailnetAutoAdd.tried == nil {
		tailnetAutoAdd.tried, tailnetAutoAdd.running, tailnetAutoAdd.failed = map[string]time.Time{}, map[string]bool{}, map[string]json.RawMessage{}
	}
	due := !tailnetAutoAdd.running[device.ID] && time.Since(tailnetAutoAdd.tried[device.ID]) >= time.Minute
	if due {
		tailnetAutoAdd.tried[device.ID] = time.Now()
		tailnetAutoAdd.running[device.ID] = true
	}
	failed := tailnetAutoAdd.failed[device.ID]
	tailnetAutoAdd.Unlock()
	if failed != nil {
		var replacement any
		if devices, ok := status["devices"].([]any); ok && index < len(devices) && json.Unmarshal(failed, &replacement) == nil {
			devices[index] = replacement
		}
	}
	if !due {
		return status, nil
	}
	if err = authorizeTailnetKey(s.home, view.PublicKey); err != nil {
		tailnetAutoAdd.Lock()
		tailnetAutoAdd.running[device.ID] = false
		tailnetAutoAdd.Unlock()
		status["this_device_error"] = "authorize_failed"
		return status, nil
	}
	go s.tailnetAddSelf(device.ID, name)
	return status, nil
}

// tailnetAddSelf asks the cloud computer to sign in to this Mac and keeps a
// failed result for later status calls. A success is saved on the cloud
// computer, so status reports it from then on.
func (s *daemonServices) tailnetAddSelf(id, name string) {
	added, err := s.tailnetCall(context.Background(), cloudops.Operation{Kind: "tailnet_add", Tailnet: &tailnet.Request{ID: id, User: name}}, 25*time.Second)
	var result tailnet.Device
	tailnetAutoAdd.Lock()
	defer tailnetAutoAdd.Unlock()
	tailnetAutoAdd.running[id] = false
	if err != nil || json.Unmarshal(added, &result) != nil || result.Computer == nil {
		return
	}
	if result.Computer.Error == "" {
		delete(tailnetAutoAdd.failed, id)
		return
	}
	tailnetAutoAdd.failed[id] = added
}

// markThisDevice sets this Mac's computer error in a status payload.
func markThisDevice(status map[string]any, index int, code string) {
	devices, ok := status["devices"].([]any)
	if !ok || index >= len(devices) {
		return
	}
	device, ok := devices[index].(map[string]any)
	if !ok {
		return
	}
	computer, _ := device["computer"].(map[string]any)
	if computer == nil {
		computer = map[string]any{}
	}
	computer["error"] = code
	device["computer"] = computer
}

// tailscalePrefs reads this Mac's Tailscale preferences as JSON. The app's
// bundled CLI works for both the App Store and the standalone app. Started
// by the daemon, without a shell's environment, that binary tries to open
// the GUI ("CLIError error 3") unless TAILSCALE_BE_CLI is set.
var tailscalePrefs = func(ctx context.Context) ([]byte, error) {
	for _, path := range []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale"} {
		if _, err := os.Stat(path); err == nil {
			command := exec.CommandContext(ctx, path, "debug", "prefs")
			command.Env = append(os.Environ(), "TAILSCALE_BE_CLI=1")
			return command.Output()
		}
	}
	return nil, os.ErrNotExist
}

var shieldsCache struct {
	sync.Mutex
	at time.Time
	up bool
}

// localShieldsUp reports whether Tailscale on this Mac blocks incoming
// connections (ShieldsUp). Unknown counts as no. The answer is kept 10 s,
// because the panel polls status every few seconds.
func localShieldsUp() bool {
	shieldsCache.Lock()
	defer shieldsCache.Unlock()
	if time.Since(shieldsCache.at) < 10*time.Second {
		return shieldsCache.up
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var prefs struct{ ShieldsUp bool }
	raw, err := tailscalePrefs(ctx)
	shieldsCache.up = err == nil && json.Unmarshal(raw, &prefs) == nil && prefs.ShieldsUp
	shieldsCache.at = time.Now()
	return shieldsCache.up
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
