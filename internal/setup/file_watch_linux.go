//go:build linux

package setup

import (
	"context"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func statChangeNanos(stat *syscall.Stat_t) int64 {
	return stat.Ctim.Sec*int64(time.Second) + stat.Ctim.Nsec
}

// platformKernelWake watches the parent directories with inotify and wakes
// only for events naming one of the watched files, so a busy agent directory
// does not cause wake-ups. Directories that do not exist yet are added when
// they appear (checked every few seconds).
func platformKernelWake(ctx context.Context, paths []string) <-chan struct{} {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil
	}
	names := map[string]map[string]bool{}
	for _, path := range paths {
		directory := filepath.Dir(path)
		if names[directory] == nil {
			names[directory] = map[string]bool{}
		}
		names[directory][filepath.Base(path)] = true
	}
	wake := make(chan struct{}, 1)
	go func() {
		defer unix.Close(fd)
		watches := map[int]string{}
		watched := map[string]bool{}
		const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_CREATE | unix.IN_DELETE | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF
		arm := func() {
			for directory := range names {
				if watched[directory] {
					continue
				}
				if wd, err := unix.InotifyAddWatch(fd, directory, mask); err == nil {
					watches[wd] = directory
					watched[directory] = true
				}
			}
		}
		arm()
		buffer := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
		lastArm := time.Now()
		for ctx.Err() == nil {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, 500); err != nil && err != unix.EINTR {
				return
			}
			if time.Since(lastArm) > 5*time.Second {
				arm()
				lastArm = time.Now()
			}
			if poll[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, err := unix.Read(fd, buffer)
			if err != nil || n <= 0 {
				continue
			}
			relevant := false
			for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
				event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
				nameBytes := buffer[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+int(event.Len)]
				offset += unix.SizeofInotifyEvent + int(event.Len)
				directory := watches[int(event.Wd)]
				if event.Mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_IGNORED) != 0 {
					delete(watches, int(event.Wd))
					delete(watched, directory)
					relevant = true
					continue
				}
				name := string(nameBytes)
				for i := 0; i < len(name); i++ {
					if name[i] == 0 {
						name = name[:i]
						break
					}
				}
				if names[directory][name] {
					relevant = true
				}
			}
			if relevant {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return wake
}
