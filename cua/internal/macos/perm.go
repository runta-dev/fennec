//go:build darwin

// Package macos holds the small macOS queries the daemon needs directly:
// permission state and the main display's size. None of them prompts the
// user or changes anything, except RequestPermissions, which runs only when
// the user asks for the system prompts (`fennec-cua permissions --request`).
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

static int requestAccessibility(void) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *values[] = { kCFBooleanTrue };
	CFDictionaryRef opts = CFDictionaryCreate(NULL, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	int ok = AXIsProcessTrustedWithOptions(opts) ? 1 : 0;
	CFRelease(opts);
	return ok;
}
#include <dlfcn.h>

typedef CGImageRef (*windowListImageFn)(CGRect, CGWindowListOption, CGWindowID, CGWindowImageOption);

// directCapture takes one 1x1 point capture through the legacy window list
// call, resolved at run time (newer SDKs mark it unavailable). On macOS 15
// and later the first such capture of an app makes macOS ask the user once
// whether the app may capture the screen directly; the answer is kept.
// It answers 1 when an image came back, 0 when none, -1 when the call is
// missing.
static int directCapture(void) {
	windowListImageFn f = (windowListImageFn)dlsym(RTLD_DEFAULT, "CGWindowListCreateImage");
	if (f == NULL) return -1;
	CGImageRef img = f(CGRectMake(0, 0, 1, 1), kCGWindowListOptionOnScreenOnly, kCGNullWindowID, kCGWindowImageDefault);
	if (img == NULL) return 0;
	CGImageRelease(img);
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

// RequestPermissions asks macOS for the Accessibility and Screen Recording
// grants of this process (its app bundle), showing the system prompts for
// those not granted yet; a prompt also lists the app in System Settings so
// the user can turn it on. It reports the grants as they are now; macOS
// applies a new Screen Recording grant only after the process restarts.
func RequestPermissions() (accessibility, screenRecording bool) {
	accessibility = C.requestAccessibility() != 0
	screenRecording = ScreenCaptureAllowed()
	if !screenRecording {
		screenRecording = bool(C.CGRequestScreenCaptureAccess())
	}
	return accessibility, screenRecording
}

// RequestDirectCapture takes one tiny screen capture so that macOS asks the
// user, once, whether the app may capture the screen directly (macOS 15 and
// later). cua-driver, embedded in the app, never raises that alert itself
// and refuses captures until it has been answered. It runs only when the
// user asks for the system prompts. It reports whether an image came back.
func RequestDirectCapture() bool { return C.directCapture() == 1 }
