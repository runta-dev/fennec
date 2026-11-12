//go:build darwin && cgo

package record

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation -framework Carbon
#include <stdlib.h>
#include <ApplicationServices/ApplicationServices.h>
#include <Carbon/Carbon.h>

#define TAP_QUEUE 64

typedef struct {
	int kind;
	double x, y;
	int64_t clicks, code;
	uint64_t flags;
	UniChar text[8];
	int textLen;
} tapEvent;

typedef struct {
	CFMachPortRef port;
	CFRunLoopSourceRef src;
	tapEvent q[TAP_QUEUE];
	int n;
} tapCtx;

// The callback runs on the tap's own thread inside tapTurn; it only copies
// the event, which Go reads once the run loop turn returns.
static CGEventRef tapHeard(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *user) {
	tapCtx *c = (tapCtx *)user;
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (c->port) CGEventTapEnable(c->port, true);
		return event;
	}
	if (c->n >= TAP_QUEUE) return event;
	tapEvent *e = &c->q[c->n++];
	e->kind = (int)type;
	CGPoint p = CGEventGetLocation(event);
	e->x = p.x;
	e->y = p.y;
	e->clicks = CGEventGetIntegerValueField(event, kCGMouseEventClickState);
	e->code = CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
	e->flags = (uint64_t)CGEventGetFlags(event);
	e->textLen = 0;
	if (type == kCGEventKeyDown) {
		UniCharCount n = 0;
		CGEventKeyboardGetUnicodeString(event, 8, &n, e->text);
		e->textLen = n > 8 ? 8 : (int)n;
	}
	return event;
}

static tapCtx *tapStart(void) {
	tapCtx *c = calloc(1, sizeof(tapCtx));
	CGEventMask mask = CGEventMaskBit(kCGEventLeftMouseDown) | CGEventMaskBit(kCGEventRightMouseDown) | CGEventMaskBit(kCGEventKeyDown);
	c->port = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap, kCGEventTapOptionListenOnly, mask, tapHeard, c);
	if (!c->port) {
		free(c);
		return NULL;
	}
	c->src = CFMachPortCreateRunLoopSource(NULL, c->port, 0);
	CFRunLoopAddSource(CFRunLoopGetCurrent(), c->src, kCFRunLoopCommonModes);
	CGEventTapEnable(c->port, true);
	return c;
}

// tapTurn runs the run loop until one event is handled or 0.1 s pass.
static void tapTurn(void) { CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.1, true); }

static void tapEnd(tapCtx *c) {
	CGEventTapEnable(c->port, false);
	CFRunLoopRemoveSource(CFRunLoopGetCurrent(), c->src, kCFRunLoopCommonModes);
	CFMachPortInvalidate(c->port);
	CFRelease(c->src);
	CFRelease(c->port);
	free(c);
}

static int tapSecureInput(void) { return IsSecureEventInputEnabled() ? 1 : 0; }
static int tapAllowed(void) { return CGPreflightListenEventAccess() ? 1 : 0; }
static void tapAsk(void) { CGRequestListenEventAccess(); }
*/
import "C"

import (
	"errors"
	"runtime"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"fennec/cua/internal/ax"
)

const (
	cgLeftMouseDown  = 1
	cgRightMouseDown = 3
	cgKeyDown        = 10
)

var errInputMonitoring = errors.New("recording needs Input Monitoring: allow Fennec under System Settings > Privacy & Security > Input Monitoring, then start again")

// PlatformListen is the listen-only CGEventTap with accessibility reads.
var PlatformListen Listen = func(sink func(Input)) (func(), error) {
	return startTap(axReader{}, sink)
}

// reader is what the tap reads from the accessibility tree as input comes.
type reader interface {
	// HitAt reads the element at a desktop point.
	HitAt(x, y float64) *Hit
	// Typing answers the process with the keyboard focus and whether a
	// password field has it.
	Typing() (pid int, secure bool)
	// Focused reads the focused field; secure forces its secure flag.
	Focused(secure bool) *Field
}

// startTap runs a tap on its own locked OS thread until stop.
func startTap(r reader, sink func(Input)) (func(), error) {
	if C.tapAllowed() == 0 {
		C.tapAsk()
		return nil, errInputMonitoring
	}
	ready := make(chan error, 1)
	var done atomic.Bool
	ended := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(ended)
		c := C.tapStart()
		if c == nil {
			ready <- errInputMonitoring
			return
		}
		ready <- nil
		scale := ax.BackingScale()
		var run keyRun
		watch := secureWatch{on: C.tapSecureInput() != 0}
		for !done.Load() {
			C.tapTurn()
			n := int(c.n)
			for i := 0; i < n; i++ {
				if in, ok := heard(&c.q[i], scale, r, &run); ok {
					sink(in)
				}
			}
			c.n = 0
			if watch.turn(C.tapSecureInput() != 0) {
				pid, _ := r.Typing()
				sink(Input{Kind: InputPassword, At: time.Now(), PID: pid, Focus: r.Focused(true)})
			}
		}
		C.tapEnd(c)
	}()
	if err := <-ready; err != nil {
		<-ended
		return nil, err
	}
	return func() {
		done.Store(true)
		<-ended
	}, nil
}

func heard(e *C.tapEvent, scale float64, r reader, run *keyRun) (Input, bool) {
	now := time.Now()
	flags := uint64(e.flags)
	switch int(e.kind) {
	case cgLeftMouseDown, cgRightMouseDown:
		x, y := float64(e.x)*scale, float64(e.y)*scale
		run.click()
		return Input{Kind: InputClick, At: now, X: x, Y: y, Right: int(e.kind) == cgRightMouseDown, Count: int(e.clicks), Mods: modsOf(flags), Hit: r.HitAt(x, y)}, true
	case cgKeyDown:
		pid, secure := r.Typing()
		text := ""
		if !secure {
			units := unsafe.Slice((*uint16)(unsafe.Pointer(&e.text[0])), int(e.textLen))
			text = string(utf16.Decode(units))
		}
		first := run.key(text, flags)
		var focus *Field
		if first {
			focus = r.Focused(secure)
		}
		return Input{Kind: InputKey, At: now, Code: int(e.code), Text: text, Mods: modsOf(flags), Secret: secure, PID: pid, Focus: focus, Read: first}, true
	}
	return Input{}, false
}

// secureInputOn reports whether some process holds secure keyboard input.
func secureInputOn() bool { return C.tapSecureInput() != 0 }
