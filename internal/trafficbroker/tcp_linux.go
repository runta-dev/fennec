package trafficbroker

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// queuedOutput observes only bytes not yet sent, not unacknowledged flight.
// Counting flight would stop bulk for an entire RTT even on a healthy link.
// The pipe and TCP snapshots are not atomic and exclude sshd's internal buffers.
func queuedOutput(output, tcp int) (uint64, error) {
	info, err := unix.GetsockoptTCPInfo(tcp, unix.IPPROTO_TCP, unix.TCP_INFO)
	if err != nil {
		return 0, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(output, &stat); err != nil {
		return 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return 0, fmt.Errorf("TCP queue guard requires an output pipe")
	}
	pipeBytes, err := unix.IoctlGetInt(output, unix.TIOCINQ)
	return uint64(info.Notsent_bytes) + uint64(max(pipeBytes, 0)), err
}

// discoverSSHSocket binds the observer to this exact output pipe and validated
// SSH tuple. Merely inheriting SSH_CONNECTION or having an SSH ancestor is not
// sufficient. The duplicate is observation-only; never read or write its data,
// change its options, or toggle its shared nonblocking flag.
func discoverSSHSocket(output *os.File) int {
	tuple := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(tuple) != 4 {
		return -1
	}
	peerIP, localIP := net.ParseIP(tuple[0]), net.ParseIP(tuple[2])
	peerPort, peerErr := strconv.Atoi(tuple[1])
	localPort, localErr := strconv.Atoi(tuple[3])
	if peerIP == nil || localIP == nil || peerErr != nil || localErr != nil || peerPort < 1 || peerPort > 65535 || localPort < 1 || localPort > 65535 {
		return -1
	}
	var pipe unix.Stat_t
	if unix.Fstat(int(output.Fd()), &pipe) != nil || pipe.Mode&unix.S_IFMT != unix.S_IFIFO {
		return -1
	}
	pid := os.Getppid()
	for depth := 0; depth < 8 && pid > 1; depth++ {
		root := fmt.Sprintf("/proc/%d", pid)
		exe, _ := os.Readlink(filepath.Join(root, "exe"))
		name := filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
		if name == "sshd" || name == "sshd-session" {
			if fd := socketFromSSHProcess(pid, pipe, peerIP, peerPort, localIP, localPort); fd >= 0 {
				return fd
			}
		}
		status, err := os.ReadFile(filepath.Join(root, "status"))
		if err != nil {
			return -1
		}
		pid = 0
		for _, line := range strings.Split(string(status), "\n") {
			if value, ok := strings.CutPrefix(line, "PPid:"); ok {
				pid, _ = strconv.Atoi(strings.TrimSpace(value))
				break
			}
		}
	}
	return -1
}

func socketFromSSHProcess(pid int, pipe unix.Stat_t, peerIP net.IP, peerPort int, localIP net.IP, localPort int) int {
	process, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return -1
	}
	defer unix.Close(process)
	root := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(root)
	if err != nil {
		return -1
	}
	ownsPipe := false
	var sockets []int
	defer func() {
		for _, fd := range sockets {
			if fd >= 0 {
				unix.Close(fd)
			}
		}
	}()
	for _, entry := range entries {
		link, _ := os.Readlink(filepath.Join(root, entry.Name()))
		if link != fmt.Sprintf("pipe:[%d]", pipe.Ino) && !strings.HasPrefix(link, "socket:[") {
			continue
		}
		source, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fd, err := unix.PidfdGetfd(process, source, 0)
		if err != nil {
			continue
		}
		unix.CloseOnExec(fd)
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFIFO && stat.Ino == pipe.Ino && stat.Dev == pipe.Dev {
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			ownsPipe = ownsPipe || (err == nil && flags&unix.O_ACCMODE == unix.O_RDONLY)
		}
		peer, peerErr := unix.Getpeername(fd)
		local, localErr := unix.Getsockname(fd)
		if peerErr == nil && localErr == nil && matchesAddress(peer, peerIP, peerPort) && matchesAddress(local, localIP, localPort) {
			if _, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO); err == nil {
				sockets = append(sockets, fd)
				continue
			}
		}
		unix.Close(fd)
	}
	if !ownsPipe || len(sockets) != 1 {
		return -1
	}
	fd := sockets[0]
	sockets[0] = -1
	return fd
}

func matchesAddress(address unix.Sockaddr, ip net.IP, port int) bool {
	switch address := address.(type) {
	case *unix.SockaddrInet4:
		return port == address.Port && ip.Equal(net.IP(address.Addr[:]))
	case *unix.SockaddrInet6:
		return port == address.Port && ip.Equal(net.IP(address.Addr[:]))
	}
	return false
}
