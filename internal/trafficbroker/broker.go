// Package trafficbroker shares a sending budget between local processes. It
// owns nonblocking writes to passed pipe/socket descriptors, never a blocking
// SSH Channel.Write. This limits application egress before transport buffering;
// it cannot drain bytes already buffered by sshd or unrelated applications.
package trafficbroker

import (
	"context"
	"encoding/binary"
	"errors"
	"fennec/internal/traffic"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	version    = 2
	maxData    = 1792
	maxFlow    = 256
	maxPending = 64
	overhead   = 96
	headerSize = 13 // request ID, class, flow length, data length
)

// Listen requires a private, current-user-owned directory. Version the socket
// name when protocol or mandatory scheduling semantics change.
func Listen(path string) (*net.UnixListener, error) {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Lstat(filepath.Dir(path), &st); err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("traffic broker requires a private owned directory")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// Serve uses one aggregate budget for every registered destination. Cancellation
// closes IPC connections, waits for queued callbacks, then releases passed FDs.
// It never closes a descriptor while a queued callback can still access it.
func Serve(ctx context.Context, listener *net.UnixListener, config traffic.Config) error {
	return serve(ctx, listener, config, 0)
}

func serve(ctx context.Context, listener *net.UnixListener, config traffic.Config, idle time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	schedule, err := traffic.New(ctx, config)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	clients := make(chan struct{}, 128)
	var lifetime sync.Mutex
	lastRelease := time.Now()
	if idle > 0 {
		go func() {
			ticker := time.NewTicker(min(idle, time.Second))
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					lifetime.Lock()
					if len(clients) == 0 && time.Since(lastRelease) >= idle {
						cancel()
					}
					lifetime.Unlock()
				}
			}
		}()
	}
	var serial uint64
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			cancel()
			return err
		}
		lifetime.Lock()
		if ctx.Err() != nil {
			lifetime.Unlock()
			conn.Close()
			return nil
		}
		select {
		case clients <- struct{}{}:
		default:
			lifetime.Unlock()
			conn.Close()
			continue
		}
		lifetime.Unlock()
		serial++
		prefix := strconv.FormatUint(serial, 10) + "/"
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { lifetime.Lock(); <-clients; lastRelease = time.Now(); lifetime.Unlock() }()
			serveClient(ctx, conn, schedule, prefix)
		}()
	}
}

func receiveFD(conn *net.UnixConn) (int, int, error) {
	var marker [1]byte
	control := make([]byte, unix.CmsgSpace(16*4))
	n, oobn, flags, _, err := conn.ReadMsgUnix(marker[:], control)
	if err != nil {
		return -1, -1, err
	}
	messages, parseErr := unix.ParseSocketControlMessage(control[:oobn])
	var descriptors []int
	for _, message := range messages {
		rights, e := unix.ParseUnixRights(&message)
		if e != nil {
			parseErr = e
		} else {
			descriptors = append(descriptors, rights...)
		}
	}
	for _, fd := range descriptors {
		unix.CloseOnExec(fd)
	}
	if parseErr != nil || flags&unix.MSG_CTRUNC != 0 || n != 1 || marker[0] != version || len(descriptors) < 1 || len(descriptors) > 2 {
		for _, fd := range descriptors {
			unix.Close(fd)
		}
		return -1, -1, fmt.Errorf("invalid traffic descriptor registration")
	}
	fd := descriptors[0]
	unix.CloseOnExec(fd)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err == nil {
		kind := stat.Mode & unix.S_IFMT
		if kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
			err = fmt.Errorf("traffic output must be a pipe or socket")
		}
	}
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	tcpFD := -1
	if len(descriptors) == 2 {
		tcpFD = descriptors[1]
		if err == nil {
			_, err = queuedOutput(fd, tcpFD)
		}
	}
	if err != nil {
		for _, descriptor := range descriptors {
			unix.Close(descriptor)
		}
		return -1, -1, err
	}
	return fd, tcpFD, nil
}

func serveClient(parent context.Context, conn *net.UnixConn, schedule *traffic.Scheduler, prefix string) {
	ctx, cancel := context.WithCancel(parent)
	defer conn.Close()
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fd, tcpFD, err := receiveFD(conn)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	var guarded byte
	if tcpFD >= 0 {
		defer unix.Close(tcpFD)
		guarded = 1
	}
	if _, err = conn.Write([]byte{version, guarded}); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	var workers sync.WaitGroup
	defer workers.Wait()
	// Cancel before waiting, including when a peer disconnects while its output
	// pipe is full. A descriptor is released only after every callback finishes.
	defer cancel()
	slots := make(chan struct{}, maxPending)
	var replies sync.Mutex
	var idsMu sync.Mutex
	active := make(map[uint64]bool)
	// Only the scheduler's single callback runner accesses owner. A partial
	// socket packet owns this destination until its tail is written; unrelated
	// destinations can still progress. Pipes <= PIPE_BUF are atomic.
	var owner uint64
	for {
		var header [headerSize]byte
		if _, err = io.ReadFull(conn, header[:]); err != nil {
			return
		}
		id := binary.BigEndian.Uint64(header[:8])
		class := traffic.Class(header[8])
		flowLen := int(binary.BigEndian.Uint16(header[9:11]))
		dataLen := int(binary.BigEndian.Uint16(header[11:13]))
		if id == 0 || class > traffic.Bulk || flowLen > maxFlow || dataLen < 1 || dataLen > maxData {
			return
		}
		body := make([]byte, flowLen+dataLen)
		if _, err = io.ReadFull(conn, body); err != nil {
			return
		}
		idsMu.Lock()
		duplicate := active[id]
		if !duplicate {
			active[id] = true
		}
		idsMu.Unlock()
		if duplicate {
			return
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			data := body[flowLen:]
			var writeErr error
			for len(data) > 0 && writeErr == nil {
				writeErr = schedule.TryWrite(ctx, prefix+string(body[:flowLen]), class, len(data)+overhead, func() error {
					if owner != 0 && owner != id {
						return traffic.ErrWouldBlock
					}
					// Finish partial packets before admitting another packet. Only
					// whole bulk packets wait for transport backlog to drain.
					if tcpFD >= 0 && class == traffic.Bulk && owner == 0 {
						queued, err := queuedOutput(fd, tcpFD)
						if err != nil {
							return err
						}
						if queued > 1024 {
							return traffic.ErrWouldBlock
						}
					}
					n, e := unix.Write(fd, data)
					if n > 0 {
						data = data[n:]
						if len(data) > 0 {
							owner = id
						} else {
							owner = 0
						}
						// Charge the whole reservation even for a partial socket write.
						// Retrying its tail is conservative, never an uncharged burst.
						return nil
					}
					if errors.Is(e, unix.EAGAIN) || errors.Is(e, unix.EWOULDBLOCK) || errors.Is(e, unix.EINTR) {
						return traffic.ErrWouldBlock
					}
					if e == nil {
						e = io.ErrNoProgress
					}
					return e
				})
			}
			if writeErr != nil {
				cancel()
				return
			}
			var reply [8]byte
			binary.BigEndian.PutUint64(reply[:], id)
			replies.Lock()
			_, err := conn.Write(reply[:])
			replies.Unlock()
			idsMu.Lock()
			delete(active, id)
			idsMu.Unlock()
			if err != nil {
				cancel()
			}
		}()
	}
}
