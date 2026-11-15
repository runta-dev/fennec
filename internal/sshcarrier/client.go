package sshcarrier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/connectivity"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config contains only public routing and identity data. The API token and
// private key are never passed in argv, a configuration file, or logs.
type Config struct {
	Address   string `json:"address"`
	User      string `json:"user"`
	HostKey   []byte `json:"host_key"`
	ClientKey []byte `json:"client_key"`
	KeyPath   string `json:"key_path"`
	Scope     string `json:"scope"`
	NoDelay   bool   `json:"no_delay"`
}

func (c Config) key() string {
	return c.keyForEpoch(workerCompatibilityEpoch)
}

func (c Config) keyForEpoch(epoch int) string {
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("fennec-ssh-carrier-v%d\x00", epoch)), data...))
	return hex.EncodeToString(sum[:12])
}

func privateDirectory(home string) (string, error) {
	directory := filepath.Join(home, ".config/fennec/ipc")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("private transport directory required")
	}
	return directory, nil
}

func (c Config) signer() (ssh.Signer, error) {
	file, err := os.OpenFile(c.KeyPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private transport key required")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 {
		return nil, fmt.Errorf("invalid transport key")
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), c.ClientKey) {
		return nil, fmt.Errorf("transport key changed")
	}
	return signer, nil
}

// Dial leases a private local channel multiplexer. Callers must perform fresh
// API ownership/key checks before issuing commands, including on cache hits.
func Dial(ctx context.Context, home string, config Config) (*ssh.Client, error) {
	directory, err := privateDirectory(home)
	if err != nil {
		return nil, err
	}
	signer, err := config.signer()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(directory, "transport-"+config.key())
	if client, err := dialLocal(ctx, base+".sock", signer); err == nil {
		return client, nil
	}
	lock, err := os.OpenFile(base+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if client, err := dialLocal(ctx, base+".sock", signer); err == nil {
		return client, nil
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(directory, ".transport-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	closeErr := temp.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := os.Rename(temp.Name(), base+".json"); err != nil {
		return nil, err
	}
	if err := startWorker(ctx, base, config.key()); err != nil {
		return nil, err
	}
	for {
		client, err := dialLocal(ctx, base+".sock", signer)
		if err == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func dialLocal(ctx context.Context, socket string, signer ssh.Signer) (*ssh.Client, error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := raw.Write([]byte{leaseRequest}); err != nil {
		raw.Close()
		return nil, err
	}
	conn, channels, requests, err := ssh.NewClientConn(raw, socket, &ssh.ClientConfig{
		User: localLeaseUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
	})
	if err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	if ctx.Err() != nil {
		conn.Close()
		return nil, ctx.Err()
	}
	return ssh.NewClient(conn, channels, requests), nil
}

// Revoke closes retained transports on explicit logout/environment changes.
// A daemon replacement deliberately does not call it. The socket is protected
// like control.sock: only the owning local OS account can access it.
func Revoke(home string) error {
	directory, err := privateDirectory(home)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var first error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "transport-") || !strings.HasSuffix(entry.Name(), ".sock") {
			continue
		}
		conn, err := net.DialTimeout("unix", filepath.Join(directory, entry.Name()), time.Second)
		if err != nil {
			continue
		} // An exited worker may leave a stale socket.
		conn.SetDeadline(time.Now().Add(time.Second))
		_, err = conn.Write([]byte{stopRequest})
		if err == nil {
			var reply [1]byte
			_, err = conn.Read(reply[:]) // EOF confirms worker cancellation.
			if err == io.EOF {
				err = nil
			}
		}
		conn.Close()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RunWorker accepts only its own canonical, private configuration. It never
// reads API credentials; upstream host and client keys are pinned by the daemon.
func RunWorker(ctx context.Context, home, path string) error {
	directory, err := privateDirectory(home)
	if err != nil {
		return err
	}
	if filepath.Dir(path) != directory {
		return fmt.Errorf("unexpected transport configuration")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return fmt.Errorf("private transport configuration required")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	file.Close()
	if err != nil || len(data) > 16384 {
		return fmt.Errorf("invalid transport configuration")
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	base := filepath.Join(directory, "transport-"+config.key())
	if path != base+".json" {
		return fmt.Errorf("transport scope mismatch")
	}
	signer, err := config.signer()
	if err != nil {
		return err
	}
	host, err := ssh.ParsePublicKey(config.HostKey)
	if err != nil {
		return err
	}
	// Worker lifetime lock is separate from the startup lock held by Dial.
	lock, err := os.OpenFile(base+".worker", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	os.Remove(base + ".sock")
	listener, err := net.Listen("unix", base+".sock")
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(base+".sock", 0600); err != nil {
		return err
	}
	network := connectivity.NewMonitor(nil, time.Second)
	network.Start(ctx)
	health := defaultHealthPolicy()
	health.network = network
	return serve(ctx, listener, signer, func(ctx context.Context) (*ssh.Client, error) {
		raw, err := (&net.Dialer{KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", config.Address)
		if err != nil {
			return nil, err
		}
		if tcp, ok := raw.(*net.TCPConn); ok {
			tcp.SetNoDelay(config.NoDelay)
		}
		stop := context.AfterFunc(ctx, func() { raw.Close() })
		defer stop()
		conn, channels, requests, err := ssh.NewClientConn(raw, config.Address, &ssh.ClientConfig{
			User: config.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host),
		})
		if err != nil {
			raw.Close()
			return nil, err
		}
		if ctx.Err() != nil {
			conn.Close()
			return nil, ctx.Err()
		}
		return ssh.NewClient(conn, channels, requests), nil
	}, defaultIdle, health)
}
