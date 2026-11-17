package localssh

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The five-second bound is unchanged. It applies only to the dedicated health
// stream, which carries neither user data nor approval requests.
const macRelayProbeTimeout = 5 * time.Second

var errMacConnectionGone = errors.New("Mac SSH connection is no longer registered or paired")

type macRelayHealth struct {
	mu         sync.Mutex
	client     *ssh.Client
	registered bool
	closed     bool
}

func (u *macUpstream) closeHealth(data *ssh.Client) {
	if value, ok := u.health.LoadAndDelete(data); ok {
		health := value.(*macRelayHealth)
		health.mu.Lock()
		health.closed = true
		control := health.client
		health.mu.Unlock()
		control.Close()
	}
}

func (h *macRelayHealth) probe(ctx context.Context, data, control *ssh.Client) error {
	stop := context.AfterFunc(ctx, func() { control.Close() })
	defer stop()
	kind, payload := "keepalive@openssh.com", []byte(nil)
	if h.registered {
		kind = probeConnectionHealth
		payload = ssh.Marshal(struct{ Session []byte }{data.SessionID()})
	}
	ok, _, err := control.SendRequest(kind, true, payload)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	// An old Mac rejects unknown keepalives but still replies over this isolated
	// connection. New Macs positively verify the exact data-connection session ID.
	if h.registered && !ok {
		return errMacConnectionGone
	}
	return nil
}

func (u *macUpstream) probeHealth(data *ssh.Client, ended <-chan struct{}) bool {
	value, ok := u.health.Load(data)
	if !ok {
		return false
	}
	health := value.(*macRelayHealth)
	// A failed control stream alone does not establish that the data stream died.
	// Reconnect control once, without replaying any data operation or listener.
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(u.ctx, macRelayProbeTimeout)
		result := make(chan error, 1)
		health.mu.Lock()
		control, closed := health.client, health.closed
		health.mu.Unlock()
		if closed {
			cancel()
			return false
		}
		go func() {
			if attempt > 0 {
				fresh, err := u.dialSSH(ctx)
				if err != nil {
					result <- err
					return
				}
				health.mu.Lock()
				if health.closed || ctx.Err() != nil {
					health.mu.Unlock()
					fresh.Close()
					result <- io.EOF
					return
				}
				previous := health.client
				health.client = fresh
				health.mu.Unlock()
				previous.Close()
				control = fresh
			}
			result <- health.probe(ctx, data, control)
		}()
		var err error
		select {
		case err = <-result:
		case <-ended:
			cancel()
			return false
		case <-ctx.Done():
			err = ctx.Err()
		}
		cancel()
		if err == nil {
			return true
		}
		if errors.Is(err, errMacConnectionGone) || u.ctx.Err() != nil {
			return false
		}
	}
	return false
}
