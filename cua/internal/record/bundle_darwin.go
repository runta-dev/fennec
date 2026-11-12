//go:build darwin && cgo

package record

/*
#include <libproc.h>
#include <sys/param.h>

static int pidPath(int pid, char *buf, int size) { return proc_pidpath(pid, buf, (uint32_t)size); }
*/
import "C"

import "unsafe"

// BundleStem names a process's app bundle without .app, whatever the
// system language; empty when it has none.
func BundleStem(pid int) string {
	if pid <= 0 {
		return ""
	}
	buf := make([]byte, 4*C.MAXPATHLEN)
	n := C.pidPath(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n <= 0 {
		return ""
	}
	return bundleStem(string(buf[:n]))
}
