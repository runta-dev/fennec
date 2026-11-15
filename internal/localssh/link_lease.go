package localssh

import (
	"context"

	"golang.org/x/crypto/ssh"
)

// A borrowed connection belongs to the daemon's shared recovery lifecycle.
// Link cancellation closes its session/forwarding channels, never peer TUIs.
type linkSSHLease struct {
	client          *ssh.Client
	owned           bool
	stop, stopSetup func() bool
}

func newLinkSSHLease(ctx, setup context.Context, client *ssh.Client, owned bool) linkSSHLease {
	lease := linkSSHLease{client: client, owned: owned, stop: func() bool { return true }, stopSetup: func() bool { return true }}
	if owned {
		lease.stop = context.AfterFunc(ctx, func() { client.Close() })
		lease.stopSetup = context.AfterFunc(setup, func() { client.Close() })
	}
	return lease
}

func (l linkSSHLease) finishSetup() bool { return l.stopSetup() }
func (l linkSSHLease) close() {
	l.stopSetup()
	l.stop()
	if l.owned {
		l.client.Close()
	}
}
