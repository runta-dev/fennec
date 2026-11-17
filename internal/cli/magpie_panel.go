package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/controlplane"
	"fennec/internal/magpieweb"
	"fennec/internal/terminalstream"
	"fmt"
	"net"
	"sync"
)

func installMagpiePanel(ctx context.Context, s *daemonServices, c *controlplane.Controller, settings *desktopSettings) {
	var mu sync.Mutex
	var panel *magpieweb.Local
	var owner uint64
	var runtime string
	context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if panel != nil {
			panel.Close()
		}
	})
	c.Additional["magpie.panel"] = func(request context.Context, _ json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		life, _, epoch, ok := s.account.Session()
		if !ok || !settings.snapshot().(desktopPreferences).Magpie {
			return nil, fmt.Errorf("Enable Magpie to open its control panel")
		}
		s.mu.Lock()
		channel, current, id := s.channel, s.epoch, s.config.RuntimeID
		s.mu.Unlock()
		if channel == nil || epoch != current || id == "" {
			return nil, fmt.Errorf("Cloud computer is connecting")
		}
		valid := func() bool {
			s.mu.Lock()
			same := s.epoch == epoch && s.config.RuntimeID == id
			s.mu.Unlock()
			return same && life.Err() == nil && settings.snapshot().(desktopPreferences).Magpie
		}
		dial := func(ctx context.Context) (net.Conn, error) {
			if !valid() {
				return nil, fmt.Errorf("Magpie is unavailable")
			}
			transport, err := channel.get(ctx)
			if err != nil {
				return nil, err
			}
			if err = transport.Ready(ctx); err != nil {
				return nil, err
			}
			if !transport.Supports(terminalstream.CapabilityMagpieWeb) {
				return nil, fmt.Errorf("Cloud computer is updating")
			}
			return transport.DialSocket(ctx, terminalstream.SocketMagpieWeb, "")
		}
		// Confirm the web process is ready before opening the browser.
		conn, err := dial(request)
		if err != nil {
			return nil, err
		}
		conn.Close()
		if panel == nil || owner != epoch || runtime != id {
			if panel != nil {
				panel.Close()
			}
			panel = &magpieweb.Local{}
			owner = epoch
			runtime = id
		}
		link, err := panel.URL(life, dial, valid)
		if err != nil {
			return nil, err
		}
		return map[string]string{"url": link}, nil
	}
}
