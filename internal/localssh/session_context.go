package localssh

import (
	"context"

	"golang.org/x/crypto/ssh"
)

// Canceling one connector setup must not close its borrowed SSH connection.
// If a channel-open reply arrives after cancellation, release that channel only.
func connectorSession(ctx context.Context, client *ssh.Client) (*ssh.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		session *ssh.Session
		err     error
	}
	done := make(chan result, 1)
	go func() { session, err := client.NewSession(); done <- result{session, err} }()
	select {
	case reply := <-done:
		if err := ctx.Err(); err != nil {
			if reply.session != nil {
				go reply.session.Close()
			}
			return nil, err
		}
		return reply.session, reply.err
	case <-ctx.Done():
		go func() {
			if reply := <-done; reply.session != nil {
				reply.session.Close()
			}
		}()
		return nil, ctx.Err()
	}
}
