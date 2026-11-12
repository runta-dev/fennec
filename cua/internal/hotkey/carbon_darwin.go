//go:build darwin

package hotkey

/*
#cgo LDFLAGS: -framework Carbon
#include <Carbon/Carbon.h>

extern void fennecHotkeyFired(UInt32 id);

static OSStatus onHotkey(EventHandlerCallRef next, EventRef event, void *data) {
	EventHotKeyID hk;
	if (GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof(hk), NULL, &hk) == noErr) {
		fennecHotkeyFired(hk.id);
	}
	return noErr;
}

static OSStatus installHandler(void) {
	static int installed = 0;
	if (installed) return noErr;
	EventTypeSpec spec = { kEventClassKeyboard, kEventHotKeyPressed };
	OSStatus st = InstallApplicationEventHandler(NewEventHandlerUPP(onHotkey), 1, &spec, NULL, NULL);
	if (st == noErr) installed = 1;
	return st;
}

static OSStatus registerHotkey(UInt32 code, UInt32 mods, UInt32 id, EventHotKeyRef *out) {
	OSStatus st = installHandler();
	if (st != noErr) return st;
	EventHotKeyID hk = { 'fcua', id };
	return RegisterEventHotKey(code, mods, hk, GetApplicationEventTarget(), kEventHotKeyExclusive, out);
}

static void unregisterHotkey(EventHotKeyRef ref) { UnregisterEventHotKey(ref); }
*/
import "C"

import (
	"fmt"
	"sync"
)

var (
	firedMu sync.Mutex
	fired   = map[uint32]func(){}
	nextID  uint32
)

//export fennecHotkeyFired
func fennecHotkeyFired(id C.UInt32) {
	firedMu.Lock()
	f := fired[uint32(id)]
	firedMu.Unlock()
	if f != nil {
		go f()
	}
}

// Carbon registers hotkeys with Carbon's RegisterEventHotKey. Presses are
// delivered on the application event loop, so the process's main thread
// must run a Cocoa or Carbon run loop.
type Carbon struct{}

// Register registers h exclusively; another app holding the same
// combination makes it fail with ErrInUse.
func (Carbon) Register(h Hotkey, fire func()) (func(), error) {
	firedMu.Lock()
	nextID++
	id := nextID
	fired[id] = fire
	firedMu.Unlock()
	var ref C.EventHotKeyRef
	if st := C.registerHotkey(C.UInt32(h.Code), C.UInt32(h.Modifiers), C.UInt32(id), &ref); st != C.noErr {
		firedMu.Lock()
		delete(fired, id)
		firedMu.Unlock()
		return nil, fmt.Errorf("%w (status %d)", ErrInUse, int(st))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			C.unregisterHotkey(ref)
			firedMu.Lock()
			delete(fired, id)
			firedMu.Unlock()
		})
	}, nil
}
