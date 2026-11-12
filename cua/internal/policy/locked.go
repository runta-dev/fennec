package policy

import (
	"strings"

	"fennec/cua/internal/protocol"
)

// LoginWindow is the process that owns the lock screen.
const LoginWindow = "loginwindow"

// LockedRefusal is the error for input while the screen is locked or shows
// the screen saver.
func LockedRefusal(lockedInput bool) *protocol.Error {
	msg := "the screen is locked: macOS hides the windows and routes input to the lock screen, and fennec-cua cannot unlock the Mac or reach the lock screen's password field; nothing was sent"
	hint := "unlock the Mac over Screen Sharing or at the machine, then try again"
	if lockedInput {
		hint += "; locked-screen typing reaches only the window that had focus before the lock, with background keys or typing"
	}
	return &protocol.Error{Code: protocol.ErrScreenLocked, Message: msg, Hint: hint}
}

// LockedDelivered says where locked-screen keys or typing went.
func LockedDelivered(app string) string {
	if strings.EqualFold(app, LoginWindow) {
		return "the screen is locked: the input went to the lock screen"
	}
	if app == "" {
		app = "the target app"
	}
	return "the screen is locked: the input went to the key window of " + app + " as it was before the lock; nothing could be read back"
}

// Locked decides a step while the screen is locked. Without the
// locked-input policy every input is refused. With it, under background or
// auto input, typing and single keys (no chords) go to the window that had
// focus before the lock, provided the driver supports locked delivery;
// clicks and every other action are refused, and so is anything aimed at
// loginwindow, which has no focused window to type into.
func (p *Policy) Locked(kind string, keys []string, mode, app string, supported bool) *protocol.Error {
	if !p.LockedInput || mode == protocol.InputForeground {
		return LockedRefusal(p.LockedInput)
	}
	switch kind {
	case "type":
	case "key":
		if len(keys) != 1 {
			e := LockedRefusal(true)
			e.Message = "the screen is locked and key chords are not sent through it; nothing was sent"
			return e
		}
	default:
		return LockedRefusal(true)
	}
	if strings.EqualFold(app, LoginWindow) {
		return &protocol.Error{Code: protocol.ErrScreenLocked, Message: "loginwindow has no focused window to type into; nothing was sent", Hint: LockedRefusal(true).Hint}
	}
	if !supported {
		return &protocol.Error{Code: protocol.ErrUnavailable, Message: "the bundled cua-driver cannot deliver input through a locked screen; nothing was sent", Hint: LockedRefusal(true).Hint}
	}
	return nil
}
