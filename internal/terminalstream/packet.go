package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/traffic"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

const CapabilityTrafficPackets = "traffic_packets_v1"

const (
	packetPlainBytes = 1536
	packetDataBytes  = 1024
	packetOverhead   = 96 // Conservative SSH framing and TCP/IP allowance per write.
	packetMessageMax = (4 << 20) + 65536
	packetBufferMax  = 8 << 20
)

type packetSender struct {
	enabled       atomic.Bool
	serial        atomic.Uint64
	ioMu          sync.Mutex
	flowMu        sync.Mutex
	tails         map[string]chan struct{}
	slots         chan struct{}
	bulkSlots     chan struct{}
	budgetMu      sync.Mutex
	budgetChanged chan struct{}
	reserved      int
	schedule      *traffic.Scheduler
}

// sendFrames preserves logical message order per stream, but releases the
// transport between fragments. A large operation reply cannot monopolize the
// byte stream while a different stream has a pong or input confirmation ready.
func (s *writeScheduler) sendFrames(output io.Writer, frames ...Frame) error {
	if !s.packets.enabled.Load() {
		return s.write(frames[0].ID, urgentFrames(frames), func() error {
			s.packets.ioMu.Lock()
			defer s.packets.ioMu.Unlock()
			var batch bytes.Buffer
			encoder := json.NewEncoder(&batch)
			for _, frame := range frames {
				if err := encoder.Encode(frame); err != nil {
					return err
				}
			}
			n, err := output.Write(batch.Bytes())
			if err == nil && n != batch.Len() {
				err = io.ErrShortWrite
			}
			return err
		})
	}
	p := s.packets
	if !urgentFrames(frames) && !frames[0].interactive {
		select {
		case p.bulkSlots <- struct{}{}:
			defer func() { <-p.bulkSlots }()
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	select {
	case p.slots <- struct{}{}:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	defer func() { <-p.slots }()
	flow := frames[0].ID
	done := make(chan struct{})
	p.flowMu.Lock()
	previous := p.tails[flow]
	p.tails[flow] = done
	p.flowMu.Unlock()
	defer func() {
		p.flowMu.Lock()
		if p.tails[flow] == done {
			delete(p.tails, flow)
		}
		close(done)
		p.flowMu.Unlock()
	}()
	// Canceled queued messages still wait for their predecessor to release its
	// buffer. Closing a middle ticket early would allow its successor to pass.
	if previous != nil {
		<-previous
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	// Preserve the receipt/first-paint coalescing benefit when the batch fits
	// one bounded packet. Every line remains independently decodable.
	if len(frames) > 1 {
		var batch bytes.Buffer
		encoder := json.NewEncoder(&batch)
		class := traffic.Bulk
		for _, frame := range frames {
			if err := encoder.Encode(frame); err != nil {
				return err
			}
			if frame.interactive || urgentFrames([]Frame{frame}) {
				class = traffic.Interactive
			}
			if batch.Len() > packetPlainBytes {
				break
			}
		}
		if batch.Len() <= packetPlainBytes {
			return s.sendPacket(output, flow, class, batch.Bytes()[:batch.Len()-1])
		}
	}
	for _, frame := range frames {
		data, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if len(data) > packetMessageMax {
			return fmt.Errorf("terminal message exceeds packet budget")
		}
		class := traffic.Bulk
		if frame.interactive || len(data) <= packetPlainBytes && urgentFrames([]Frame{frame}) {
			class = traffic.Interactive
		}
		if len(data) <= packetPlainBytes {
			if err := s.sendPacket(output, flow, class, data); err != nil {
				return err
			}
			continue
		}
		id := strconv.FormatUint(p.serial.Add(1), 10)
		if err := s.fragment(output, flow, class, id, data); err != nil {
			return err
		}
	}
	return nil
}

func (s *writeScheduler) fragment(output io.Writer, flow string, class traffic.Class, id string, data []byte) error {
	p := s.packets
	for {
		p.budgetMu.Lock()
		if len(data) <= packetBufferMax-p.reserved {
			p.reserved += len(data)
			p.budgetMu.Unlock()
			break
		}
		changed := p.budgetChanged
		p.budgetMu.Unlock()
		select {
		case <-changed:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	defer func() {
		p.budgetMu.Lock()
		p.reserved -= len(data)
		close(p.budgetChanged)
		p.budgetChanged = make(chan struct{})
		p.budgetMu.Unlock()
	}()
	for offset := 0; offset < len(data); {
		n := min(packetDataBytes, len(data)-offset)
		encoded, err := json.Marshal(Frame{Type: "fragment", ID: id, Seq: uint64(offset), Bytes: len(data), Data: data[offset : offset+n]})
		if err != nil {
			return err
		}
		if err := s.sendPacket(output, flow, class, encoded); err != nil {
			return err
		}
		offset += n
	}
	return nil
}

func (s *writeScheduler) sendPacket(output io.Writer, flow string, class traffic.Class, data []byte) error {
	data = append(data, '\n')
	// Enqueue directly into a shared broker. Waiting for its bulk quota in
	// our serial scheduler would prevent later pongs from entering its queue.
	if shared, ok := output.(interface {
		WritePacket(context.Context, string, traffic.Class, []byte) error
	}); ok {
		return shared.WritePacket(s.ctx, flow, class, data)
	}
	return s.packets.schedule.Write(s.ctx, flow, class, len(data)+packetOverhead, func() error {
		s.packets.ioMu.Lock()
		defer s.packets.ioMu.Unlock()
		n, err := output.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	})
}

type packetAssembly struct {
	total int
	data  []byte
}

// packetDecoder is owned by the existing reader goroutine. Incomplete large
// replies do not block delivery of complete control frames between fragments.
// Reserve the advertised size before allocating so many partial messages
// cannot grow memory beyond the connection's aggregate budget.
type packetDecoder struct {
	pending  map[string]*packetAssembly
	reserved int
	maximum  int
}

func (d *packetDecoder) decode(data []byte) (Frame, bool, error) {
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return frame, false, fmt.Errorf("invalid terminal frame")
	}
	if frame.Type != "fragment" {
		return frame, true, nil
	}
	maximum := d.maximum
	if maximum == 0 {
		maximum = packetMessageMax
	}
	if frame.ID == "" || len(frame.ID) > 32 || frame.Bytes <= 0 || frame.Bytes > maximum ||
		len(frame.Data) == 0 || len(frame.Data) > packetDataBytes || frame.Seq > uint64(frame.Bytes) {
		return Frame{}, false, fmt.Errorf("invalid terminal fragment")
	}
	if d.pending == nil {
		d.pending = make(map[string]*packetAssembly)
	}
	part := d.pending[frame.ID]
	if part == nil {
		if frame.Seq != 0 || len(d.pending) >= 64 || frame.Bytes > packetBufferMax-d.reserved {
			return Frame{}, false, fmt.Errorf("terminal fragment assembly budget exceeded")
		}
		part = &packetAssembly{total: frame.Bytes, data: make([]byte, 0, frame.Bytes)}
		d.reserved += frame.Bytes
		d.pending[frame.ID] = part
	}
	if frame.Bytes != part.total || frame.Seq != uint64(len(part.data)) || len(frame.Data) > part.total-len(part.data) {
		return Frame{}, false, fmt.Errorf("terminal fragments are out of order")
	}
	part.data = append(part.data, frame.Data...)
	if len(part.data) < part.total {
		return Frame{}, false, nil
	}
	delete(d.pending, frame.ID)
	d.reserved -= part.total
	frame = Frame{}
	if json.Unmarshal(part.data, &frame) != nil || frame.Type == "fragment" {
		return Frame{}, false, fmt.Errorf("invalid assembled terminal frame")
	}
	return frame, true, nil
}
