package launcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func connectionIdentity(directory string) ([32]byte, error) {
	host, err := os.ReadFile(filepath.Join(directory, "known_hosts"))
	if err != nil || len(host) == 0 {
		return [32]byte{}, fmt.Errorf("host identity unavailable")
	}
	key, err := os.ReadFile(filepath.Join(directory, "runtime_key"))
	if err != nil || len(key) == 0 {
		return [32]byte{}, fmt.Errorf("client identity unavailable")
	}
	data := append(append(host, 0), key...)
	return sha256.Sum256(data), nil
}

// Handshaking opens no remote session or command. Readiness is withheld until
// both fresh authorization and the handshake succeed. Failure closes our master.
func prepareConnection(ctx context.Context, authorize, connect func(context.Context) error, identityUnchanged func() bool, stop func()) error {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	authorized, connected := make(chan error, 1), make(chan error, 1)
	go func() { authorized <- authorize(work) }()
	go func() { connected <- connect(work) }()
	var authErr error
	select {
	case authErr = <-authorized:
	case <-ctx.Done():
		authErr = ctx.Err()
	}
	if authErr != nil {
		cancel()
		<-connected
		stop()
		return authErr
	}
	if !identityUnchanged() {
		cancel()
		<-connected
		stop()
		return connect(ctx)
	}
	var connectionErr error
	select {
	case connectionErr = <-connected:
	case <-ctx.Done():
		cancel()
		<-connected
		stop()
		return ctx.Err()
	}
	if connectionErr != nil {
		stop()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return connect(ctx)
	}
	if err := ctx.Err(); err != nil {
		stop()
		return err
	}
	return nil
}
