//go:build scenario && darwin

package scenario

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>

static void postMouse(CGEventType type, double x, double y, CGMouseButton button) {
	CGEventRef e = CGEventCreateMouseEvent(NULL, type, CGPointMake(x, y), button);
	if (e == NULL) return;
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void postKey(CGKeyCode code, int down) {
	CGEventRef e = CGEventCreateKeyboardEvent(NULL, code, down != 0);
	if (e == NULL) return;
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static int setWindowPoint(int pid, const char *title, const char *attr, double x, double y) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	CFArrayRef wins = NULL;
	int ok = 0;
	if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef *)&wins) == kAXErrorSuccess && wins != NULL) {
		CFStringRef want = CFStringCreateWithCString(NULL, title, kCFStringEncodingUTF8);
		CFStringRef name = CFStringCreateWithCString(NULL, attr, kCFStringEncodingUTF8);
		for (CFIndex i = 0; i < CFArrayGetCount(wins) && !ok; i++) {
			AXUIElementRef w = (AXUIElementRef)CFArrayGetValueAtIndex(wins, i);
			CFStringRef t = NULL;
			if (AXUIElementCopyAttributeValue(w, kAXTitleAttribute, (CFTypeRef *)&t) != kAXErrorSuccess || t == NULL) continue;
			if (CFStringCompare(t, want, 0) == kCFCompareEqualTo) {
				AXValueRef v;
				if (CFStringCompare(name, kAXSizeAttribute, 0) == kCFCompareEqualTo) {
					CGSize s = CGSizeMake(x, y);
					v = AXValueCreate(kAXValueTypeCGSize, &s);
				} else {
					CGPoint p = CGPointMake(x, y);
					v = AXValueCreate(kAXValueTypeCGPoint, &p);
				}
				ok = AXUIElementSetAttributeValue(w, name, v) == kAXErrorSuccess;
				CFRelease(v);
			}
			CFRelease(t);
		}
		CFRelease(want);
		CFRelease(name);
		CFRelease(wins);
	}
	CFRelease(app);
	return ok;
}
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"

	"fennec/cua/internal/ax"
)

// The harness's own input stands in for a user: it is posted at the HID
// level like a person's, so fennec-cua sees it as user activity. It runs in
// the test VM only.

func toPoints(x, y int) (C.double, C.double) {
	s := ax.BackingScale()
	if s <= 0 {
		s = 1
	}
	return C.double(float64(x) / s), C.double(float64(y) / s)
}

// userMove moves the pointer to a desktop point (pixels of the main display).
func userMove(x, y int) {
	px, py := toPoints(x, y)
	C.postMouse(C.kCGEventMouseMoved, px, py, C.kCGMouseButtonLeft)
}

// userClick clicks at a desktop point.
func userClick(x, y int) {
	px, py := toPoints(x, y)
	C.postMouse(C.kCGEventMouseMoved, px, py, C.kCGMouseButtonLeft)
	time.Sleep(30 * time.Millisecond)
	C.postMouse(C.kCGEventLeftMouseDown, px, py, C.kCGMouseButtonLeft)
	time.Sleep(30 * time.Millisecond)
	C.postMouse(C.kCGEventLeftMouseUp, px, py, C.kCGMouseButtonLeft)
}

// userKey taps a virtual key code (53 is Escape, 36 Return).
func userKey(code int) {
	C.postKey(C.CGKeyCode(code), 1)
	time.Sleep(20 * time.Millisecond)
	C.postKey(C.CGKeyCode(code), 0)
}

// moveWindow sets a window's top-left corner in points.
func moveWindow(pid int, title string, x, y float64) error {
	return setWindow(pid, title, "AXPosition", x, y)
}

// resizeWindow sets a window's size in points.
func resizeWindow(pid int, title string, w, h float64) error {
	return setWindow(pid, title, "AXSize", w, h)
}

func setWindow(pid int, title, attr string, a, b float64) error {
	ct, ca := C.CString(title), C.CString(attr)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(ca))
	if C.setWindowPoint(C.int(pid), ct, ca, C.double(a), C.double(b)) == 0 {
		return errors.New("could not set " + attr + " of " + title)
	}
	return nil
}
