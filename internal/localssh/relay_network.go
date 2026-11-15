package localssh

import "context"

type relayNetwork interface {
	Online() bool
	Changes() <-chan struct{}
}

func cancelOfflineRelay(ctx context.Context, network relayNetwork, cancel context.CancelFunc) {
	for {
		changed := network.Changes()
		if !network.Online() {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}
