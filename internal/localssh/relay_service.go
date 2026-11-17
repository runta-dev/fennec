package localssh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A protocol revision, not a binary hash: compatible builds reuse the service.
const relayRevision = "go-relay-v1"

type relayState struct {
	Bridge   int    `json:"bridge_port"`
	Agent    int    `json:"agent_port"`
	PID      int    `json:"pid"`
	Revision string `json:"revision"`
}

// EnsureRelay serializes repair and never signals a process without checking its
// device-specific command line. It also migrates the legacy Python service.
func EnsureRelay(ctx context.Context, home, device string, force ...bool) (LinkBootstrapResult, error) {
	if !deviceIDPattern.MatchString(device) {
		return LinkBootstrapResult{}, fmt.Errorf("invalid device")
	}
	base := filepath.Join(home, ".local/fennec/relay-"+device)
	if err := os.MkdirAll(base, 0700); err != nil {
		return LinkBootstrapResult{}, err
	}
	lock, err := os.OpenFile(filepath.Join(base, "setup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return LinkBootstrapResult{}, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return LinkBootstrapResult{}, err
		}
		select {
		case <-ctx.Done():
			return LinkBootstrapResult{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := inspectExistingRelay(ctx, home, device, os.ReadFile)
	if err != nil {
		return current, err
	}
	if current.Ready && (len(force) == 0 || !force[0]) {
		return current, nil
	}
	path := filepath.Join(base, "state.json")
	raw, readErr := os.ReadFile(path)
	if readErr == nil {
		var state relayState
		if json.Unmarshal(raw, &state) != nil || state.PID <= 1 {
			return current, fmt.Errorf("invalid relay state")
		}
		args, e := os.ReadFile(filepath.Join("/proc", strconv.Itoa(state.PID), "cmdline"))
		if e == nil && len(args) > 0 {
			if !relayProcessMatches(args, base, device) {
				return current, fmt.Errorf("relay process identity mismatch")
			}
			if e = syscall.Kill(state.PID, syscall.SIGTERM); e != nil && e != syscall.ESRCH {
				return current, e
			}
			// The new service uses fresh ephemeral ports; old streams are already
			// unhealthy. Do not wait for unrelated sessions or stop the resident.
		} else if e != nil && !os.IsNotExist(e) {
			return current, e
		}
	} else if !os.IsNotExist(readErr) {
		return current, readErr
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return current, err
	}
	logFile, err := os.OpenFile(filepath.Join(base, "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return current, err
	}
	defer logFile.Close()
	binary := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	child := exec.Command(binary, "relay-serve", device)
	child.Env = append(os.Environ(), "HOME="+home)
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = child.Start(); err != nil {
		return current, err
	}
	go child.Wait()
	for {
		current, err = inspectExistingRelay(ctx, home, device, os.ReadFile)
		if err != nil || current.Ready {
			return current, err
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func relayProcessMatches(raw []byte, base, device string) bool {
	args := splitRelayArgs(raw)
	binary := filepath.Join(filepath.Dir(base), "bin/fennec-terminal")
	return (len(args) == 4 && string(args[0]) == binary && string(args[1]) == "relay-serve" && string(args[2]) == device) ||
		(containsArg(args, filepath.Join(base, "relay.py")) && containsArg(args, "--state") && containsArg(args, filepath.Join(base, "state.json")))
}

// ServeRelay owns only loopback byte forwarding; SSH authentication stays on Mac.
func ServeRelay(ctx context.Context, home, device string) error {
	if !deviceIDPattern.MatchString(device) {
		return fmt.Errorf("invalid device")
	}
	bridges, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer bridges.Close()
	agents, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer agents.Close()
	base := filepath.Join(home, ".local/fennec/relay-"+device)
	state := relayState{bridges.Addr().(*net.TCPAddr).Port, agents.Addr().(*net.TCPAddr).Port, os.Getpid(), relayRevision}
	raw, _ := json.Marshal(state)
	if err = os.WriteFile(filepath.Join(base, "state.json.next"), raw, 0600); err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(base, "state.json.next"), filepath.Join(base, "state.json")); err != nil {
		return err
	}
	return serveRelayListeners(ctx, bridges, agents)
}

func serveRelayListeners(ctx context.Context, bridges, agents net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { bridges.Close(); agents.Close() })
	defer stop()
	var mu sync.Mutex
	var parked []net.Conn
	wake := make(chan struct{}, 1)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range parked {
			c.Close()
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			c, e := bridges.Accept()
			if e != nil {
				cancel()
				return
			}
			mu.Lock()
			live := parked[:0]
			for _, p := range parked {
				if idleRelayAlive(p) {
					live = append(live, p)
				} else {
					p.Close()
				}
			}
			parked = live
			if len(parked) >= 16 {
				parked[0].Close()
				parked = parked[1:]
			}
			parked = append(parked, c)
			mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()
	capacity := make(chan struct{}, maxRelayConnections)
	for {
		agent, e := agents.Accept()
		if e != nil {
			return e
		}
		select {
		case capacity <- struct{}{}:
		default:
			agent.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-capacity }()
			defer agent.Close()
			claim, cancelClaim := context.WithTimeout(ctx, 5*time.Second)
			defer cancelClaim()
			var bridge net.Conn
			for bridge == nil {
				mu.Lock()
				for len(parked) > 0 {
					p := parked[0]
					parked = parked[1:]
					if idleRelayAlive(p) {
						bridge = p
						break
					}
					p.Close()
				}
				mu.Unlock()
				if bridge != nil {
					break
				}
				select {
				case <-claim.Done():
					return
				case <-wake:
				}
			}
			defer bridge.Close()
			stop := context.AfterFunc(ctx, func() { agent.Close(); bridge.Close() })
			defer stop()
			bridge.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, e := bridge.Write([]byte{'F'}); e != nil {
				return
			}
			bridge.SetWriteDeadline(time.Time{})
			done := make(chan struct{})
			go func() { io.Copy(bridge, agent); bridge.(*net.TCPConn).CloseWrite(); close(done) }()
			io.Copy(agent, bridge)
			agent.(*net.TCPConn).CloseWrite()
			<-done
		}()
	}
}

func idleRelayAlive(c net.Conn) bool {
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		return false
	}
	raw, e := tcp.SyscallConn()
	if e != nil {
		return false
	}
	alive := false
	e = raw.Control(func(fd uintptr) {
		var b [1]byte
		_, _, err := unix.Recvfrom(int(fd), b[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
		alive = err == unix.EAGAIN || err == unix.EWOULDBLOCK
	})
	return e == nil && alive
}
