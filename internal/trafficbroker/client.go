package trafficbroker

import (
	"context"
	"encoding/binary"
	"fennec/internal/traffic"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Client shares one output destination with the broker. Passing a descriptor
// sets its shared open-file description nonblocking; the caller must route all
// writes to that destination through this client. Close leaves the original
// descriptor owned by the caller. Each flow's caller preserves packet order.
// A canceled submitted write closes this client: its remote write outcome is
// uncertain, so replaying a packet on a replacement client is unsafe.
type Client struct {
	conn             *net.UnixConn
	writes           sync.Mutex
	mu               sync.Mutex
	serial           uint64
	pending          map[uint64]chan error
	slots, bulkSlots chan struct{}
	done             chan struct{}
	once             sync.Once
	tcpGuarded       bool
}

func Open(ctx context.Context, path string, output *os.File) (*Client, error) {
	tcpFD := discoverSSHSocket(output)
	if tcpFD >= 0 {
		defer unix.Close(tcpFD)
	}
	return openWithTCP(ctx, path, output, tcpFD)
}

// TCPQueueGuarded reports an attached, validated TCP queue observer. Unsupported
// environments retain the ordinary scheduler and advertise no guard capability.
func (c *Client) TCPQueueGuarded() bool { return c.tcpGuarded }

func openWithTCP(ctx context.Context, path string, output *os.File, tcpFD int) (*Client, error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	conn := raw.(*net.UnixConn)
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fds := []int{int(output.Fd())}
	if tcpFD >= 0 {
		fds = append(fds, tcpFD)
	}
	if _, _, err = conn.WriteMsgUnix([]byte{version}, unix.UnixRights(fds...), nil); err != nil {
		return nil, err
	}
	var reply [2]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		return nil, err
	}
	if reply[0] != version || reply[1] > 1 || (reply[1] == 1) != (tcpFD >= 0) {
		return nil, fmt.Errorf("incompatible traffic broker")
	}
	conn.SetDeadline(time.Time{})
	c := &Client{conn: conn, pending: make(map[uint64]chan error), slots: make(chan struct{}, maxPending), bulkSlots: make(chan struct{}, maxPending-8), done: make(chan struct{})}
	c.tcpGuarded = reply[1] == 1
	success = true
	go c.read()
	return c, nil
}
func (c *Client) Close() error {
	c.once.Do(func() { c.conn.Close(); close(c.done) })
	return nil
}
func (c *Client) read() {
	defer c.Close()
	for {
		var reply [8]byte
		if _, err := io.ReadFull(c.conn, reply[:]); err != nil {
			return
		}
		c.mu.Lock()
		ack := c.pending[binary.BigEndian.Uint64(reply[:])]
		c.mu.Unlock()
		if ack == nil {
			return
		}
		select {
		case ack <- nil:
		default:
			return
		}
	}
}
func (c *Client) WritePacket(ctx context.Context, flow string, class traffic.Class, data []byte) error {
	if len(data) == 0 || len(data) > maxData || len(flow) > maxFlow || class > traffic.Bulk {
		return traffic.ErrPacketSize
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if class == traffic.Bulk {
		select {
		case c.bulkSlots <- struct{}{}:
			defer func() { <-c.bulkSlots }()
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return io.ErrClosedPipe
		}
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return io.ErrClosedPipe
	}
	c.mu.Lock()
	c.serial++
	id := c.serial
	ack := make(chan error, 1)
	c.pending[id] = ack
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	packet := make([]byte, headerSize+len(flow)+len(data))
	binary.BigEndian.PutUint64(packet[:8], id)
	packet[8] = byte(class)
	binary.BigEndian.PutUint16(packet[9:11], uint16(len(flow)))
	binary.BigEndian.PutUint16(packet[11:13], uint16(len(data)))
	copy(packet[headerSize:], flow)
	copy(packet[headerSize+len(flow):], data)
	c.writes.Lock()
	n, err := c.conn.Write(packet)
	c.writes.Unlock()
	if err != nil || n != len(packet) {
		c.Close()
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return io.ErrClosedPipe
	case err := <-ack:
		return err
	}
}

// Write supports ordinary opaque streams. Framed protocols use WritePacket
// directly so their interactive packets can preempt bulk quota waits.
func (c *Client) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		n := min(len(data), maxData)
		if err := c.WritePacket(context.Background(), "opaque", traffic.Bulk, data[:n]); err != nil {
			return written, err
		}
		data = data[n:]
		written += n
	}
	return written, nil
}
