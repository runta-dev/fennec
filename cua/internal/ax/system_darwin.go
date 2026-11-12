//go:build darwin

package ax

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <dlfcn.h>
#include <libproc.h>
#include <stdint.h>

#pragma clang diagnostic ignored "-Wdeprecated-declarations"

static int procPath(int pid, char *buf, int size) {
	return proc_pidpath(pid, buf, (uint32_t)size);
}

// The front process from the process manager: no accessibility message is
// sent, so a hung front app cannot block it, and it works off the main
// thread without a run loop.
static int frontPID(void) {
	ProcessSerialNumber psn;
	if (GetFrontProcess(&psn) != noErr) return 0;
	pid_t pid = 0;
	if (GetProcessPID(&psn, &pid) != noErr) return 0;
	return (int)pid;
}

static void *symbol(const char *name) { return dlsym(RTLD_DEFAULT, name); }

typedef int (*connFn)(void);
typedef CFArrayRef (*displaySpacesFn)(int);
typedef CFArrayRef (*spacesForWindowsFn)(int, int, CFArrayRef);

static int callConn(void *f) { return ((connFn)f)(); }

static int64_t numberOf(CFTypeRef v) {
	int64_t n = 0;
	if (v && CFGetTypeID(v) == CFNumberGetTypeID()) CFNumberGetValue((CFNumberRef)v, kCFNumberSInt64Type, &n);
	return n;
}

static CFTypeRef getKey(CFDictionaryRef d, const char *key) {
	CFStringRef k = CFStringCreateWithCString(kCFAllocatorDefault, key, kCFStringEncodingUTF8);
	CFTypeRef v = CFDictionaryGetValue(d, k);
	CFRelease(k);
	return v;
}

// managedSpaces fills ids, types and current flags for up to max Spaces of
// every display and answers how many, or -1 when the call failed.
static int managedSpaces(void *f, int cid, uint64_t *ids, int *types, int *current, int max) {
	CFArrayRef displays = ((displaySpacesFn)f)(cid);
	if (!displays) return -1;
	int n = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(displays); i++) {
		CFDictionaryRef d = CFArrayGetValueAtIndex(displays, i);
		if (CFGetTypeID(d) != CFDictionaryGetTypeID()) continue;
		int64_t cur = 0;
		CFTypeRef c = getKey(d, "Current Space");
		if (c && CFGetTypeID(c) == CFDictionaryGetTypeID()) cur = numberOf(getKey(c, "id64"));
		CFTypeRef spaces = getKey(d, "Spaces");
		if (!spaces || CFGetTypeID(spaces) != CFArrayGetTypeID()) continue;
		for (CFIndex j = 0; j < CFArrayGetCount(spaces) && n < max; j++) {
			CFDictionaryRef s = CFArrayGetValueAtIndex(spaces, j);
			if (CFGetTypeID(s) != CFDictionaryGetTypeID()) continue;
			ids[n] = (uint64_t)numberOf(getKey(s, "id64"));
			types[n] = (int)numberOf(getKey(s, "type"));
			current[n] = ids[n] != 0 && (int64_t)ids[n] == cur;
			n++;
		}
	}
	CFRelease(displays);
	return n;
}

// spacesOf fills the Space ids of one window, up to max.
static int spacesOf(void *f, int cid, uint32_t wid, uint64_t *out, int max) {
	int64_t w = wid;
	CFNumberRef num = CFNumberCreate(kCFAllocatorDefault, kCFNumberSInt64Type, &w);
	CFArrayRef arr = CFArrayCreate(kCFAllocatorDefault, (const void **)&num, 1, &kCFTypeArrayCallBacks);
	CFRelease(num);
	CFArrayRef spaces = ((spacesForWindowsFn)f)(cid, 7, arr);
	CFRelease(arr);
	if (!spaces) return 0;
	int n = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(spaces) && n < max; i++) {
		out[n++] = (uint64_t)numberOf(CFArrayGetValueAtIndex(spaces, i));
	}
	CFRelease(spaces);
	return n;
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// ProcessPath is a process's executable path.
func ProcessPath(pid int) (string, error) {
	buf := make([]byte, 4096)
	n := C.procPath(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n <= 0 {
		return "", errors.New("no executable path for the process")
	}
	return string(buf[:n]), nil
}

// FrontmostPID is the frontmost app's process, read without any
// accessibility message; 0 when unknown.
func FrontmostPID() int { return int(C.frontPID()) }

// Symbol resolves a symbol at run time; nil when it is missing.
func Symbol(name string) unsafe.Pointer {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	return C.symbol(c)
}

// spaceAPI holds the private Space calls, resolved at run time.
type spaceAPI struct {
	conn, displays, forWindows unsafe.Pointer
}

// resolveSpaces finds the Space calls through lookup, trying the SkyLight
// names first; nil when any is missing.
func resolveSpaces(lookup func(string) unsafe.Pointer) *spaceAPI {
	find := func(names ...string) unsafe.Pointer {
		for _, n := range names {
			if p := lookup(n); p != nil {
				return p
			}
		}
		return nil
	}
	api := &spaceAPI{
		conn:       find("SLSMainConnectionID", "CGSMainConnectionID"),
		displays:   find("SLSCopyManagedDisplaySpaces", "CGSCopyManagedDisplaySpaces"),
		forWindows: find("SLSCopySpacesForWindows", "CGSCopySpacesForWindows"),
	}
	if api.conn == nil || api.displays == nil || api.forWindows == nil {
		return nil
	}
	return api
}

// SpaceLookup resolves the private Space symbols; tests replace it.
var SpaceLookup = Symbol

func (a *spaceAPI) spaces() ([]Space, bool) {
	const max = 256
	var ids [max]C.uint64_t
	var types, cur [max]C.int
	cid := C.callConn(a.conn)
	n := int(C.managedSpaces(a.displays, cid, &ids[0], &types[0], &cur[0], max))
	if n < 0 {
		return nil, false
	}
	out := make([]Space, n)
	for i := range out {
		out[i] = Space{ID: uint64(ids[i]), Type: int(types[i]), Current: cur[i] != 0}
	}
	return out, true
}

func (a *spaceAPI) spacesOf(id uint32) []uint64 {
	const max = 16
	var ids [max]C.uint64_t
	cid := C.callConn(a.conn)
	n := int(C.spacesOf(a.forWindows, cid, C.uint32_t(id), &ids[0], max))
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(ids[i])
	}
	return out
}

// OtherSpaceWindows lists the app windows on other user or full-screen
// Spaces, front to back, skipping those in listed and those skip names.
// When the private Space calls are missing it answers nothing, so the
// listing degrades to the current Space instead of failing.
func OtherSpaceWindows(listed map[uint32]bool, skip func(ServerWindow) bool) ([]ServerWindow, error) {
	api := resolveSpaces(SpaceLookup)
	if api == nil {
		return nil, nil
	}
	spaces, ok := api.spaces()
	if !ok {
		return nil, nil
	}
	all, err := ServerWindows(false)
	if err != nil {
		return nil, err
	}
	return OtherSpace(all, spaces, api.spacesOf, listed, skip), nil
}

// Web-tree switches: Chromium answers AXManualAccessibility, older Electron
// AXEnhancedUserInterface.
const (
	webSwitch       = "AXManualAccessibility"
	webSwitchLegacy = "AXEnhancedUserInterface"
)

// WebTreeOn reads whether pid's web accessibility tree is switched on.
func WebTreeOn(pid int) (bool, error) {
	app := App(pid)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	v, err := app.Attrs([]string{webSwitch, webSwitchLegacy})
	if err != nil {
		return false, err
	}
	a, _ := v[0].(bool)
	b, _ := v[1].(bool)
	return a || b, nil
}

// SetWebTree switches pid's web accessibility tree on or off.
func SetWebTree(pid int, on bool) error {
	app := App(pid)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	err := app.SetBool(webSwitch, on)
	if err != nil {
		err = app.SetBool(webSwitchLegacy, on)
	}
	return err
}
