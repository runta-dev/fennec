//go:build darwin

// Package macos holds the small macOS queries the daemon needs directly:
// permission state and the main display's size. None of them prompts the
// user or changes anything.
package macos

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>

static int mainDisplayPixels(size_t *w, size_t *h) {
	CGDirectDisplayID d = CGMainDisplayID();
	CGDisplayModeRef mode = CGDisplayCopyDisplayMode(d);
	if (mode == NULL) {
		return 0;
	}
	*w = CGDisplayModeGetPixelWidth(mode);
	*h = CGDisplayModeGetPixelHeight(mode);
	CGDisplayModeRelease(mode);
	return 1;
}
*/
import "C"

// AccessibilityTrusted reports whether this process may use the
// accessibility API, without prompting.
func AccessibilityTrusted() bool { return C.AXIsProcessTrusted() != 0 }

// ScreenCaptureAllowed reports whether this process may capture the screen,
// without prompting.
func ScreenCaptureAllowed() bool { return bool(C.CGPreflightScreenCaptureAccess()) }

// MainDisplayPixels is the main display's size in pixels, which is the size
// of desktop coordinates.
func MainDisplayPixels() (w, h int, ok bool) {
	var cw, ch C.size_t
	if C.mainDisplayPixels(&cw, &ch) == 0 {
		return 0, 0, false
	}
	return int(cw), int(ch), true
}
