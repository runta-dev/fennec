package chromium

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

// processPath reads a process's executable path (kern.procargs2: argc,
// then the path).
func processPath(pid int) (string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", err
	}
	if len(buf) < 5 {
		return "", errors.New("short procargs")
	}
	_ = binary.LittleEndian.Uint32(buf[:4])
	rest := buf[4:]
	if i := bytes.IndexByte(rest, 0); i > 0 {
		return string(rest[:i]), nil
	}
	return "", errors.New("no executable path")
}
