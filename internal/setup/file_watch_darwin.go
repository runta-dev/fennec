//go:build darwin

package setup

import (
	"context"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func statChangeNanos(stat *syscall.Stat_t) int64 {
	return stat.Ctimespec.Sec*int64(time.Second) + stat.Ctimespec.Nsec
}

// platformKernelWake registers kqueue vnode events on the parent directories
// (atomic replacement, creation, removal) and on the files themselves (writes
// in place). File descriptors are opened with O_EVTONLY, which neither reads
// the file nor keeps its volume busy, and are reopened after every event
// because a replaced file is a new vnode.
func platformKernelWake(ctx context.Context, paths []string) <-chan struct{} {
	queue, err := unix.Kqueue()
	if err != nil {
		return nil
	}
	wake := make(chan struct{}, 1)
	open := map[string]int{}
	closeAll := func() {
		for path, fd := range open {
			unix.Close(fd)
			delete(open, path)
		}
	}
	register := func() {
		closeAll()
		var changes []unix.Kevent_t
		add := func(path string, fflags uint32) {
			fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
			if err != nil {
				return
			}
			open[path] = fd
			var event unix.Kevent_t
			unix.SetKevent(&event, fd, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR)
			event.Fflags = fflags
			changes = append(changes, event)
		}
		for _, directory := range watchDirectories(paths) {
			add(directory, unix.NOTE_WRITE|unix.NOTE_DELETE|unix.NOTE_RENAME)
		}
		for _, path := range paths {
			add(path, unix.NOTE_WRITE|unix.NOTE_EXTEND|unix.NOTE_DELETE|unix.NOTE_RENAME|unix.NOTE_ATTRIB)
		}
		if len(changes) > 0 {
			unix.Kevent(queue, changes, nil, nil)
		}
	}
	register()
	go func() {
		defer unix.Close(queue)
		defer closeAll()
		events := make([]unix.Kevent_t, 16)
		timeout := unix.NsecToTimespec(int64(500 * time.Millisecond))
		rearm := time.Now()
		for ctx.Err() == nil {
			n, err := unix.Kevent(queue, nil, events, &timeout)
			if err != nil && err != unix.EINTR {
				return
			}
			if n > 0 || time.Since(rearm) > 30*time.Second {
				// Re-register first (a replaced file is a new vnode; a directory
				// may have appeared), then wake: the stat check that follows
				// also covers anything that changed while re-registering.
				register()
				rearm = time.Now()
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return wake
}
