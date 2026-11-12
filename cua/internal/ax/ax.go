//go:build darwin

// Package ax wraps the macOS accessibility API and the window server's window
// list. It holds no desk policy: it reads attributes, performs actions and
// converts CoreFoundation values into Go values.
package ax

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>

// Private but long-stable: the window server id of an AX window.
extern AXError _AXUIElementGetWindow(AXUIElementRef element, CGWindowID *identifier);

static CFStringRef cfstr(const char *s) {
	return CFStringCreateWithCString(kCFAllocatorDefault, s, kCFStringEncodingUTF8);
}

static CFArrayRef cfstrs(const char **names, int n) {
	CFStringRef *items = malloc(sizeof(CFStringRef) * n);
	for (int i = 0; i < n; i++) items[i] = cfstr(names[i]);
	CFArrayRef arr = CFArrayCreate(kCFAllocatorDefault, (const void **)items, n, &kCFTypeArrayCallBacks);
	for (int i = 0; i < n; i++) CFRelease(items[i]);
	free(items);
	return arr;
}

static AXError copyMulti(AXUIElementRef e, const char **names, int n, CFArrayRef *out) {
	CFArrayRef attrs = cfstrs(names, n);
	AXError err = AXUIElementCopyMultipleAttributeValues(e, attrs, 0, out);
	CFRelease(attrs);
	return err;
}

static AXError copyAttr(AXUIElementRef e, const char *name, CFTypeRef *out) {
	CFStringRef a = cfstr(name);
	AXError err = AXUIElementCopyAttributeValue(e, a, out);
	CFRelease(a);
	return err;
}

static AXError perform(AXUIElementRef e, const char *action) {
	CFStringRef a = cfstr(action);
	AXError err = AXUIElementPerformAction(e, a);
	CFRelease(a);
	return err;
}

static AXError setBool(AXUIElementRef e, const char *name, int v) {
	CFStringRef a = cfstr(name);
	AXError err = AXUIElementSetAttributeValue(e, a, v ? kCFBooleanTrue : kCFBooleanFalse);
	CFRelease(a);
	return err;
}

static AXError setString(AXUIElementRef e, const char *name, const char *v) {
	CFStringRef a = cfstr(name);
	CFStringRef s = cfstr(v);
	AXError err = AXUIElementSetAttributeValue(e, a, s);
	CFRelease(s);
	CFRelease(a);
	return err;
}

static AXError copyActions(AXUIElementRef e, CFArrayRef *out) {
	return AXUIElementCopyActionNames(e, out);
}

// CFString to a malloc'd UTF-8 buffer; the caller frees it.
static char *utf8(CFStringRef s) {
	CFIndex len = CFStringGetLength(s);
	CFIndex max = CFStringGetMaximumSizeForEncoding(len, kCFStringEncodingUTF8) + 1;
	char *buf = malloc(max);
	if (!CFStringGetCString(s, buf, max, kCFStringEncodingUTF8)) buf[0] = 0;
	return buf;
}

static int axValue(CFTypeRef v, int kind, double *out) {
	AXValueRef av = (AXValueRef)v;
	switch (kind) {
	case 1: { CGPoint p; if (!AXValueGetValue(av, kAXValueTypeCGPoint, &p)) return 0; out[0] = p.x; out[1] = p.y; return 1; }
	case 2: { CGSize s; if (!AXValueGetValue(av, kAXValueTypeCGSize, &s)) return 0; out[0] = s.width; out[1] = s.height; return 1; }
	case 3: { CGRect r; if (!AXValueGetValue(av, kAXValueTypeCGRect, &r)) return 0; out[0] = r.origin.x; out[1] = r.origin.y; out[2] = r.size.width; out[3] = r.size.height; return 1; }
	case 4: { CFRange r; if (!AXValueGetValue(av, kAXValueTypeCFRange, &r)) return 0; out[0] = r.location; out[1] = r.length; return 1; }
	}
	return 0;
}

static int axValueKind(CFTypeRef v) {
	switch (AXValueGetType((AXValueRef)v)) {
	case kAXValueTypeCGPoint: return 1;
	case kAXValueTypeCGSize: return 2;
	case kAXValueTypeCGRect: return 3;
	case kAXValueTypeCFRange: return 4;
	default: return 0;
	}
}

static int cfNumberDouble(CFNumberRef n, double *out) {
	return CFNumberGetValue(n, kCFNumberDoubleType, out);
}

static CFTypeID typeAXUIElement(void) { return AXUIElementGetTypeID(); }
static CFTypeID typeAXValue(void) { return AXValueGetTypeID(); }
static int isTrue(CFBooleanRef b) { return CFBooleanGetValue(b); }

static CFArrayRef windowList(int onScreenOnly) {
	CGWindowListOption opt = kCGWindowListExcludeDesktopElements;
	if (onScreenOnly) opt |= kCGWindowListOptionOnScreenOnly; else opt |= kCGWindowListOptionAll;
	return CGWindowListCopyWindowInfo(opt, kCGNullWindowID);
}

static CFTypeRef dictGet(CFDictionaryRef d, const char *key) {
	CFStringRef k = cfstr(key);
	CFTypeRef v = CFDictionaryGetValue(d, k);
	CFRelease(k);
	return v;
}

static int rectFromDict(CFDictionaryRef d, double *out) {
	CGRect r;
	if (!CGRectMakeWithDictionaryRepresentation(d, &r)) return 0;
	out[0] = r.origin.x; out[1] = r.origin.y; out[2] = r.size.width; out[3] = r.size.height;
	return 1;
}

static double backingScale(void) {
	CGDirectDisplayID d = CGMainDisplayID();
	CGDisplayModeRef m = CGDisplayCopyDisplayMode(d);
	if (!m) return 1;
	double s = (double)CGDisplayModeGetPixelWidth(m) / (double)CGDisplayModeGetWidth(m);
	CGDisplayModeRelease(m);
	return s > 0 ? s : 1;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// Error is an AXError.
type Error int

// Errors worth naming.
const (
	ErrFailure            Error = C.kAXErrorFailure
	ErrIllegalArgument    Error = C.kAXErrorIllegalArgument
	ErrInvalidElement     Error = C.kAXErrorInvalidUIElement
	ErrCannotComplete     Error = C.kAXErrorCannotComplete
	ErrAttributeUnsupport Error = C.kAXErrorAttributeUnsupported
	ErrActionUnsupported  Error = C.kAXErrorActionUnsupported
	ErrNotImplemented     Error = C.kAXErrorNotImplemented
	ErrAPIDisabled        Error = C.kAXErrorAPIDisabled
	ErrNoValue            Error = C.kAXErrorNoValue
)

func (e Error) Error() string {
	switch e {
	case ErrInvalidElement:
		return "the element no longer exists"
	case ErrCannotComplete:
		return "the app did not answer accessibility in time"
	case ErrAttributeUnsupport:
		return "attribute unsupported"
	case ErrActionUnsupported:
		return "action unsupported"
	case ErrAPIDisabled:
		return "accessibility is not enabled for fennec-cua"
	case ErrNoValue:
		return "no value"
	}
	return fmt.Sprintf("accessibility error %d", int(e))
}

func axErr(code C.AXError) error {
	if code == C.kAXErrorSuccess {
		return nil
	}
	return Error(code)
}

// Element is a retained AXUIElement. Release it when done; a finalizer
// releases forgotten ones.
type Element struct {
	ref C.AXUIElementRef
}

func wrap(ref C.AXUIElementRef) *Element {
	e := &Element{ref: ref}
	runtime.SetFinalizer(e, (*Element).Release)
	return e
}

// retainWrap retains a borrowed reference.
func retainWrap(ref C.CFTypeRef) *Element {
	C.CFRetain(ref)
	return wrap(C.AXUIElementRef(ref))
}

// Release drops the element.
func (e *Element) Release() {
	if e != nil && e.ref != 0 {
		C.CFRelease(C.CFTypeRef(e.ref))
		e.ref = 0
		runtime.SetFinalizer(e, nil)
	}
}

// App is the accessibility root of a process.
func App(pid int) *Element { return wrap(C.AXUIElementCreateApplication(C.pid_t(pid))) }

// SystemWide is the system-wide element, for hit tests and the focused app.
func SystemWide() *Element { return wrap(C.AXUIElementCreateSystemWide()) }

// SetTimeout bounds how long calls to this element's app wait, in seconds.
func (e *Element) SetTimeout(seconds float64) {
	C.AXUIElementSetMessagingTimeout(e.ref, C.float(seconds))
}

// Equal reports whether two elements are the same UI object.
func (e *Element) Equal(o *Element) bool {
	return e != nil && o != nil && C.CFEqual(C.CFTypeRef(e.ref), C.CFTypeRef(o.ref)) != 0
}

// Hash is stable for equal elements.
func (e *Element) Hash() uint64 { return uint64(C.CFHash(C.CFTypeRef(e.ref))) }

// PID is the element's process.
func (e *Element) PID() (int, error) {
	var pid C.pid_t
	if err := axErr(C.AXUIElementGetPid(e.ref, &pid)); err != nil {
		return 0, err
	}
	return int(pid), nil
}

// WindowID is the window server id of a window element.
func (e *Element) WindowID() (uint32, bool) {
	var id C.CGWindowID
	if C._AXUIElementGetWindow(e.ref, &id) != C.kAXErrorSuccess || id == 0 {
		return 0, false
	}
	return uint32(id), true
}

func cstrs(names []string) (**C.char, func()) {
	arr := C.malloc(C.size_t(len(names)) * C.size_t(unsafe.Sizeof(uintptr(0))))
	items := unsafe.Slice((**C.char)(arr), len(names))
	for i, n := range names {
		items[i] = C.CString(n)
	}
	return (**C.char)(arr), func() {
		for _, p := range items {
			C.free(unsafe.Pointer(p))
		}
		C.free(arr)
	}
}

// Attrs reads several attributes in one round trip. A missing or
// unsupported attribute reads as nil.
func (e *Element) Attrs(names []string) ([]any, error) {
	if len(names) == 0 {
		return nil, nil
	}
	cnames, free := cstrs(names)
	defer free()
	var arr C.CFArrayRef
	if err := axErr(C.copyMulti(e.ref, cnames, C.int(len(names)), &arr)); err != nil {
		return nil, err
	}
	defer C.CFRelease(C.CFTypeRef(arr))
	n := int(C.CFArrayGetCount(arr))
	out := make([]any, len(names))
	for i := 0; i < n && i < len(names); i++ {
		v := C.CFArrayGetValueAtIndex(arr, C.CFIndex(i))
		out[i] = convert(C.CFTypeRef(v))
	}
	return out, nil
}

// Attr reads one attribute.
func (e *Element) Attr(name string) (any, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var v C.CFTypeRef
	if err := axErr(C.copyAttr(e.ref, cname, &v)); err != nil {
		return nil, err
	}
	defer C.CFRelease(v)
	return convert(v), nil
}

// Perform runs an action such as AXPress.
func (e *Element) Perform(action string) error {
	c := C.CString(action)
	defer C.free(unsafe.Pointer(c))
	return axErr(C.perform(e.ref, c))
}

// SetBool sets a boolean attribute such as AXFocused.
func (e *Element) SetBool(name string, v bool) error {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	b := 0
	if v {
		b = 1
	}
	return axErr(C.setBool(e.ref, c, C.int(b)))
}

// SetString sets a string attribute such as AXValue.
func (e *Element) SetString(name, v string) error {
	cn, cv := C.CString(name), C.CString(v)
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(cv))
	return axErr(C.setString(e.ref, cn, cv))
}

// Actions lists the element's action names.
func (e *Element) Actions() ([]string, error) {
	var arr C.CFArrayRef
	if err := axErr(C.copyActions(e.ref, &arr)); err != nil {
		return nil, err
	}
	defer C.CFRelease(C.CFTypeRef(arr))
	v, _ := convert(C.CFTypeRef(arr)).([]any)
	out := make([]string, 0, len(v))
	for _, s := range v {
		if s, ok := s.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// ElementAt is the element at a point in AX (top-left origin) points.
func ElementAt(x, y float64) (*Element, error) {
	sys := C.AXUIElementCreateSystemWide()
	defer C.CFRelease(C.CFTypeRef(sys))
	var ref C.AXUIElementRef
	if err := axErr(C.AXUIElementCopyElementAtPosition(sys, C.float(x), C.float(y), &ref)); err != nil {
		return nil, err
	}
	return wrap(ref), nil
}

// convert turns a borrowed CF value into Go: string, bool, float64, Point,
// Size, Rect, Range, URL, *Element, []any, or nil.
func convert(v C.CFTypeRef) any {
	if v == 0 {
		return nil
	}
	switch t := C.CFGetTypeID(v); t {
	case C.CFStringGetTypeID():
		p := C.utf8(C.CFStringRef(v))
		defer C.free(unsafe.Pointer(p))
		return C.GoString(p)
	case C.CFBooleanGetTypeID():
		return C.isTrue(C.CFBooleanRef(v)) != 0
	case C.CFNumberGetTypeID():
		var d C.double
		if C.cfNumberDouble(C.CFNumberRef(v), &d) == 0 {
			return nil
		}
		return float64(d)
	case C.CFArrayGetTypeID():
		n := int(C.CFArrayGetCount(C.CFArrayRef(v)))
		out := make([]any, n)
		for i := range out {
			out[i] = convert(C.CFTypeRef(C.CFArrayGetValueAtIndex(C.CFArrayRef(v), C.CFIndex(i))))
		}
		return out
	case C.CFURLGetTypeID():
		s := C.CFURLGetString(C.CFURLRef(v))
		p := C.utf8(s)
		defer C.free(unsafe.Pointer(p))
		return URL(C.GoString(p))
	case C.typeAXUIElement():
		return retainWrap(v)
	case C.typeAXValue():
		var d [4]C.double
		kind := C.axValueKind(v)
		if C.axValue(v, kind, &d[0]) == 0 {
			return nil
		}
		switch kind {
		case 1:
			return Point{float64(d[0]), float64(d[1])}
		case 2:
			return Size{float64(d[0]), float64(d[1])}
		case 3:
			return Rect{float64(d[0]), float64(d[1]), float64(d[2]), float64(d[3])}
		case 4:
			return Range{int(d[0]), int(d[1])}
		}
	}
	return nil
}

// ServerWindows lists windows front to back. onScreen limits it to the
// current Space's visible windows.
func ServerWindows(onScreen bool) ([]ServerWindow, error) {
	only := 0
	if onScreen {
		only = 1
	}
	arr := C.windowList(C.int(only))
	if arr == 0 {
		return nil, errors.New("the window server listed no windows")
	}
	defer C.CFRelease(C.CFTypeRef(arr))
	n := int(C.CFArrayGetCount(arr))
	out := make([]ServerWindow, 0, n)
	get := func(d C.CFDictionaryRef, key string) C.CFTypeRef {
		k := C.CString(key)
		defer C.free(unsafe.Pointer(k))
		return C.dictGet(d, k)
	}
	num := func(v C.CFTypeRef) float64 {
		if f, ok := convert(v).(float64); ok {
			return f
		}
		return 0
	}
	for i := 0; i < n; i++ {
		d := C.CFDictionaryRef(C.CFArrayGetValueAtIndex(arr, C.CFIndex(i)))
		w := ServerWindow{
			ID:    uint32(num(get(d, "kCGWindowNumber"))),
			PID:   int(num(get(d, "kCGWindowOwnerPID"))),
			Layer: int(num(get(d, "kCGWindowLayer"))),
			Alpha: num(get(d, "kCGWindowAlpha")),
		}
		w.Owner, _ = convert(get(d, "kCGWindowOwnerName")).(string)
		w.Title, _ = convert(get(d, "kCGWindowName")).(string)
		w.OnScreen, _ = convert(get(d, "kCGWindowIsOnscreen")).(bool)
		if b := get(d, "kCGWindowBounds"); b != 0 {
			var r [4]C.double
			if C.rectFromDict(C.CFDictionaryRef(b), &r[0]) != 0 {
				w.Bounds = Rect{float64(r[0]), float64(r[1]), float64(r[2]), float64(r[3])}
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// BackingScale is the main display's pixels per point; desktop coordinates
// are AX points times this.
func BackingScale() float64 { return float64(C.backingScale()) }
