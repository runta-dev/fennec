package runtime

import (
	"context"
	"errors"
	"fennec/internal/terminalinput"
	"fennec/internal/terminalstate"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Own only the Herdr attachment PTY. The agent remains owned by Herdr's server.
func runAttachment(ctx context.Context, command *exec.Cmd, input *os.File, output io.Writer) error {
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return err
	}
	defer terminalstate.Restore(input, output, state)
	size, err := pty.GetsizeFull(input)
	if err != nil {
		size = &pty.Winsize{Rows: 40, Cols: 120}
	}
	master, err := pty.StartWithSize(command, size)
	if err != nil {
		return err
	}
	defer master.Close()
	local, cancel := context.WithCancel(ctx)
	defer cancel()
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)
	done := make(chan struct{})
	go func() {
		defer close(done)
		keys := terminalinput.Decoder{}
		for local.Err() == nil {
			select {
			case <-resized:
				if size, e := pty.GetsizeFull(input); e == nil {
					_ = pty.Setsize(master, size)
				}
			default:
			}
			events := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
			_, err := unix.Poll(events, 100)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				return
			}
			if events[0].Revents == 0 {
				if pending := keys.Flush(); len(pending) > 0 {
					if _, err := master.Write(pending); err != nil {
						return
					}
				}
				continue
			}
			buffer := make([]byte, 16384)
			n, err := unix.Read(int(input.Fd()), buffer)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if n <= 0 || err != nil {
				_, _ = master.Write([]byte{28})
				return
			}
			data, detach := keys.Feed(buffer[:n])
			if _, err = master.Write(data); err != nil {
				return
			}
			if detach {
				return
			}
		}
	}()
	_, copyErr := io.Copy(output, master)
	cancel()
	<-done
	waitErr := command.Wait()
	if waitErr != nil {
		return waitErr
	}
	// PTY EOF commonly appears as EIO when the attachment exits normally.
	if copyErr != nil && !errors.Is(copyErr, syscall.EIO) && !errors.Is(copyErr, os.ErrClosed) {
		return copyErr
	}
	return nil
}
