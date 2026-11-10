package launcher

import (
	"fennec/internal/terminalstream"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"io"
	"os"
	"time"
)

// Probe locally before the first cloud frame, avoiding a WAN round trip for the
// same display query inside the remote attachment. The caller owns raw mode.
func probeDisplay(input *os.File, output *os.File) (*terminalstream.DisplayFacts, []byte, error) {
	collector := terminalstream.DisplayCollector{}
	if !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) {
		return &collector.Facts, nil, nil
	}
	if _, err := io.WriteString(output, terminalstream.DisplayQuery); err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	quiet := deadline
	buffer := make([]byte, 4096)
	total := 0
	for time.Now().Before(deadline) && total < 65536 {
		until := deadline
		if collector.Done && quiet.Before(until) {
			until = quiet
		}
		remaining := time.Until(until)
		if remaining <= 0 {
			break
		}
		poll := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, max(1, int(remaining.Milliseconds()))); err != nil {
			if err == unix.EINTR {
				continue
			}
			return nil, nil, err
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			if collector.Done {
				break
			}
			continue
		}
		n, err := unix.Read(int(input.Fd()), buffer)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return nil, nil, err
		}
		if n == 0 {
			break
		}
		total += n
		collector.Feed(buffer[:n])
		if collector.Done {
			quiet = time.Now().Add(4 * time.Millisecond)
		}
	}
	return &collector.Facts, collector.Input(), nil
}
