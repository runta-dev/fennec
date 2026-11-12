//go:build darwin

package policy

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

static double secondsSinceInput(void) {
	return CGEventSourceSecondsSinceLastEventType(kCGEventSourceStateCombinedSessionState, kCGAnyInputEventType);
}
*/
import "C"

import "time"

// IdleTime is the time since the last mouse or keyboard event of any source,
// fennec-cua's own posted input included.
func IdleTime() time.Duration {
	return time.Duration(float64(C.secondsSinceInput()) * float64(time.Second))
}
