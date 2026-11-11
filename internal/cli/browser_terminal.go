package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/browserterminal"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/sessionid"
	"fmt"
	"net"
)

func installBrowserTerminal(ctx context.Context, s *daemonServices, c *controlplane.Controller) {
	server := &browserterminal.Server{}
	context.AfterFunc(ctx, func() { server.Close() })
	c.Additional["sessions.browser"] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var params controlplane.SessionParams
		if json.Unmarshal(raw, &params) != nil || !sessionid.Valid(params.SessionID) {
			return nil, fmt.Errorf("invalid session")
		}
		lifecycle, _, epoch, ok := s.account.Session()
		if !ok {
			return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
		}
		s.mu.Lock()
		runtimeID := s.config.RuntimeID
		current := s.epoch
		s.mu.Unlock()
		if current != epoch || runtimeID == "" {
			return nil, fmt.Errorf("session service unavailable")
		}
		if params.ExpectedRuntimeID != "" && params.ExpectedRuntimeID != runtimeID {
			return nil, fmt.Errorf("session runtime mismatch")
		}
		params.ExpectedRuntimeID = runtimeID
		request, _ := json.Marshal(params)
		url, err := server.URL(lifecycle, params.SessionID, func(ctx context.Context) (net.Conn, string, error) {
			result, err := c.Handlers()["sessions.attach"](ctx, request)
			if err != nil {
				return nil, "", err
			}
			reply, ok := result.(fenServiceReply)
			if !ok || reply.Plan == nil || reply.Plan.StreamPath == "" {
				return nil, "", fmt.Errorf("attachment unavailable")
			}
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", reply.Plan.StreamPath)
			return conn, reply.Plan.StreamID, err
		}, func() browserterminal.Metadata {
			value := browserterminal.Metadata{Title: "Untitled session", Icon: "/icons/fennec.svg"}
			s.mu.Lock()
			channel := s.channel
			s.mu.Unlock()
			if channel == nil {
				return value
			}
			channel.mu.Lock()
			service := channel.service
			channel.mu.Unlock()
			if service == nil {
				return value
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			for _, session := range service.sessions {
				if session.Receipt.SessionID != params.SessionID {
					continue
				}
				value.Title = session.Title
				icons := map[string]string{"codex": "codex-color.svg", "claude": "claudecode-color.svg", "pi": "pi.svg", "kimi": "kimi.svg", "opencode": "opencode.svg", "devin": "devin.svg", "grok": "xai.svg", "cursor": "cursor.svg"}
				if icon := icons[session.Receipt.Agent]; icon != "" {
					value.Icon = "/icons/" + icon
				}
				break
			}
			return value
		})
		if err != nil {
			return nil, err
		}
		return map[string]string{"url": url}, nil
	}
}
