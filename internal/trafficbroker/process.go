package trafficbroker

import (
	"context"
	"errors"
	"fennec/internal/traffic"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Mandatory scheduling or registration changes require a new directory version.
// Old brokers drain naturally; never kill a broker writing a live session.
func directory(home string) string { return filepath.Join(home, ".config", "fennec", "traffic-v4") }
func lock(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return -1, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
func privateDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Lstat(dir, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0700 || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("traffic broker directory is not private")
	}
	return nil
}

// Run is the transport-only helper entrypoint. The lock covers socket
// replacement and the complete service lifetime, fencing concurrent startup.
func Run(ctx context.Context, home string) error {
	dir := directory(home)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	fd, err := lock(filepath.Join(dir, "owner.lock"))
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	path := filepath.Join(dir, "out.sock")
	if info, e := os.Lstat(path); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("unexpected traffic socket path")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	listener, err := Listen(path)
	if err != nil {
		return err
	}
	defer listener.Close()
	monitor := traffic.NewCapacityMonitor()
	return serve(ctx, listener, traffic.ConfigForCapacity(monitor.Detected()), 30*time.Second, monitor)
}

// Ensure starts a standalone helper lazily using the same installed executable.
// An unavailable broker is an error, never a silent unpaced fallback. Existing
// sessions keep their broker across upgrades. Startup is bounded and serialized.
func Ensure(ctx context.Context, home, executable string, output *os.File) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dir := directory(home)
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "out.sock")
	if client, err := Open(ctx, path, output); err == nil {
		return client, nil
	} else if !unavailable(err) {
		return nil, err
	}
	var launchLock int
	for {
		fd, err := lock(filepath.Join(dir, "start.lock"))
		if err == nil {
			launchLock = fd
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer unix.Close(launchLock)
	if client, err := Open(ctx, path, output); err == nil {
		return client, nil
	} else if !unavailable(err) {
		return nil, err
	}
	command := exec.Command(executable, "traffic-broker")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Do not inherit stdio/SSH channels. Logs contain startup errors only.
	log, err := os.OpenFile(filepath.Join(dir, "broker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	command.Stderr = log
	err = command.Start()
	log.Close()
	if err != nil {
		return nil, err
	}
	go command.Wait()
	for {
		client, err := Open(ctx, path, output)
		if err == nil {
			return client, nil
		}
		if !unavailable(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func unavailable(err error) bool {
	// The idle owner can retire between accept and descriptor registration.
	// No application packet has been sent yet, so reconnecting is safe here.
	if errors.Is(err, io.EOF) {
		return true
	}
	var operation *net.OpError
	return errors.As(err, &operation) && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED))
}
