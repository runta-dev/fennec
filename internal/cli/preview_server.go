package cli

import (
	"context"
	"fmt"
	"net"

	"fennec/internal/preview"
	"fennec/internal/terminalstream"
)

// installPreviewServer serves the cloud computer's previews on this Mac's
// loopback (preview.DesktopPort), so the link an agent prints opens in the
// browser from any terminal, not only in Fennec for iPhone.
func installPreviewServer(ctx context.Context, s *daemonServices) {
	desktop := &preview.Desktop{Dial: func(ctx context.Context) (net.Conn, error) {
		if _, _, _, ok := s.account.Session(); !ok {
			return nil, fmt.Errorf("sign in to Fennec")
		}
		s.mu.Lock()
		channel := s.channel
		s.mu.Unlock()
		if channel == nil {
			return nil, fmt.Errorf("cloud computer is connecting")
		}
		transport, err := channel.get(ctx)
		if err != nil {
			return nil, err
		}
		if err = transport.Ready(ctx); err != nil {
			return nil, err
		}
		if !transport.Supports(terminalstream.CapabilityPreview) {
			return nil, fmt.Errorf("cloud computer is updating")
		}
		return transport.DialSocket(ctx, terminalstream.SocketPreview, "")
	}}
	go desktop.Serve(ctx)
}
