package commandstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// OfferLocal grants one private local connection to a scoped resident dialer.
// It performs no remote operation until accepted and expires unclaimed offers.
// done closes after both copy directions and all descriptors have been retired.
func OfferLocal(ctx context.Context, directory string, dial Dialer) (string, <-chan struct{}, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", nil, fmt.Errorf("private command socket directory required")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return "", nil, err
	}
	path := filepath.Join(directory, "cmd-"+hex.EncodeToString(nonce[:]))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return "", nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return "", nil, err
	}
	listener.SetDeadline(time.Now().Add(30 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()
		stopListen := context.AfterFunc(ctx, func() { listener.Close() })
		defer stopListen()
		local, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer local.Close()
		listener.Close()
		opening, cancel := context.WithTimeout(ctx, 10*time.Second)
		remote, err := dial(opening)
		cancel()
		if err != nil {
			return
		}
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { local.Close(); remote.Close() })
		defer stop()
		copied := make(chan struct{})
		go func() { defer close(copied); io.Copy(remote, local); remote.Close(); local.Close() }()
		io.Copy(local, remote)
		local.Close()
		remote.Close()
		<-copied
	}()
	return path, done, nil
}
