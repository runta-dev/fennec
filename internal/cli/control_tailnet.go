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
	"os/user"
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
		// The primary worker computer is where agents build, compile and
		// test; tailnet.ThisMac makes it this Mac again (FEN-147).
		"tailnet.primary": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request tailnet.Request
			if json.Unmarshal(raw, &request) != nil || request.Validate(false) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Choose a computer"}
			}
			if _, err := s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_primary", Tailnet: &tailnet.Request{ID: request.ID}}, 15*time.Second); err != nil {
				return nil, err
			}
			return s.tailnetStatus(ctx, false)
		},
		"tailnet.remove": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request tailnet.Request
			if json.Unmarshal(raw, &request) != nil || request.Validate(false) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Choose a computer"}
			}
			// The full status, so this Mac's own device stays out of the list.
			if _, err := s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_remove", Tailnet: &tailnet.Request{ID: request.ID}}, 15*time.Second); err != nil {
				return nil, err
			}
			return s.tailnetStatus(ctx, false)
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

// tailnetStatus is the cloud computer's tailnet plus what only the Mac
// knows: which device is this Mac and the local user name (the add dialog's
// default). This Mac is the default worker computer through the Fennec
// relay, so the panel leaves its tailnet device out of the list; it is
// never added over the tailnet (FEN-147).
func (s *daemonServices) tailnetStatus(ctx context.Context, probe bool) (any, error) {
	raw, err := s.tailnetCall(ctx, cloudops.Operation{Kind: "tailnet_status", Tailnet: &tailnet.Request{Probe: probe}}, 25*time.Second)
	if err != nil {
		return nil, err
	}
	var status map[string]any
	if err = json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	status["local_user"] = localUser()
	var view tailnet.Status
	if json.Unmarshal(raw, &view) != nil || view.State != "running" {
		return status, nil
	}
	local := localTailnetIPs()
	for _, device := range view.Devices {
		for _, ip := range device.IPs {
			if local[ip] {
				status["this_device"] = device.ID
			}
		}
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
