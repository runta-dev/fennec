package connectivity

import (
	"context"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

// AF_ROUTE reports interface/address and routing changes, including utun VPNs,
// Wi-Fi and Ethernet. Read-only routing sockets do not require elevated access.
func watchRouteChanges(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		close(out)
		return out
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		close(out)
		return out
	}
	go func() {
		defer close(out)
		defer unix.Close(fd)
		buffer := make([]byte, 64<<10)
		for ctx.Err() == nil {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, 250); err != nil {
				if errors.Is(err, unix.EINTR) {
					continue
				}
				return
			}
			if poll[0].Revents == 0 {
				continue
			}
			n, err := unix.Read(fd, buffer)
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n == 0 {
				return
			}
			if relevantRouteChange(buffer[:n]) {
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}

func relevantRouteChange(data []byte) bool {
	for len(data) >= 4 {
		size := int(binary.NativeEndian.Uint16(data[:2]))
		if size < 4 || size > len(data) {
			return false
		}
		message := data[:size]
		data = data[size:]
		if message[2] != unix.RTM_VERSION {
			continue
		}
		switch message[3] {
		case unix.RTM_IFINFO, unix.RTM_IFINFO2, unix.RTM_NEWADDR, unix.RTM_DELADDR:
			return true
		case unix.RTM_ADD, unix.RTM_DELETE, unix.RTM_CHANGE:
			// rt_msghdr flags are at byte 8 on Darwin. Ignore ARP/neighbor
			// cache churn and cloned routes produced by ordinary traffic.
			if len(message) >= unix.SizeofRtMsghdr {
				flags := binary.NativeEndian.Uint32(message[8:12])
				if flags&(unix.RTF_LLINFO|unix.RTF_WASCLONED) == 0 {
					return true
				}
			}
		}
	}
	return false
}
