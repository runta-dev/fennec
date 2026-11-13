//go:build darwin

package desk

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <libproc.h>

// screenLocked reports whether the login session shows the lock screen.
static int screenLocked(void) {
	CFDictionaryRef d = CGSessionCopyCurrentDictionary();
	if (d == NULL) {
		return 0;
	}
	int locked = 0;
	CFTypeRef v = CFDictionaryGetValue(d, CFSTR("CGSSessionScreenIsLocked"));
	if (v != NULL && CFGetTypeID(v) == CFBooleanGetTypeID()) {
		locked = CFBooleanGetValue((CFBooleanRef)v) ? 1 : 0;
	}
	CFRelease(d);
	return locked;
}

static int pidPath(int pid, char *buf, int size) {
	return proc_pidpath(pid, buf, (uint32_t)size);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/ax"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/view"
)

// Actions is the macOS actions.Backend: accessibility through internal/ax
// and input through the bundled cua-driver. Without a driver, Client is nil
// and driver steps answer driver.ErrNoDriver while accessibility steps work.
type Actions struct {
	*Desk
	Client *driver.Client
}

// NewActions makes the backend over a desk.
func NewActions(d *Desk, c *driver.Client) *Actions { return &Actions{Desk: d, Client: c} }

func element(h view.Handle) (*ax.Element, error) {
	el, ok := h.(*ax.Element)
	if !ok || el == nil {
		return nil, errors.New("not an accessibility element")
	}
	return el, nil
}

// axFailure keeps the AXError code for actions.
func axFailure(err error, op string) error {
	var e ax.Error
	if errors.As(err, &e) {
		return &actions.AXError{Code: int(e), Op: op}
	}
	return err
}

// Perform runs an accessibility action.
func (a *Actions) Perform(ctx context.Context, h view.Handle, action string) error {
	el, err := element(h)
	if err != nil {
		return err
	}
	el.SetTimeout(MessagingTimeout.Seconds() * 4)
	return axFailure(el.Perform(action), action)
}

// ActionNames lists an element's raw action names.
func (a *Actions) ActionNames(ctx context.Context, h view.Handle) ([]string, error) {
	el, err := element(h)
	if err != nil {
		return nil, err
	}
	el.SetTimeout(MessagingTimeout.Seconds())
	return el.Actions()
}

// SelectRange sets AXSelectedTextRange.
func (a *Actions) SelectRange(ctx context.Context, h view.Handle, loc, length int) error {
	el, err := element(h)
	if err != nil {
		return err
	}
	el.SetTimeout(MessagingTimeout.Seconds() * 4)
	return axFailure(el.SetRange("AXSelectedTextRange", loc, length), "AXSelectedTextRange")
}

// SetValue writes AXValue.
func (a *Actions) SetValue(ctx context.Context, h view.Handle, value string) error {
	el, err := element(h)
	if err != nil {
		return err
	}
	// A slider or stepper holds a number and refuses a string
	// (kAXErrorIllegalArgument): write the number it parses to.
	if cur, err := el.Attr("AXValue"); err == nil {
		if _, numeric := cur.(float64); numeric || isInt(cur) {
			if f, ok := view.NumberOf(value); ok {
				return axFailure(el.SetNumber("AXValue", f), "AXValue")
			}
		}
	}
	return axFailure(el.SetString("AXValue", value), "AXValue")
}

// Select sets AXSelected.
func (a *Actions) Select(ctx context.Context, h view.Handle) error {
	el, err := element(h)
	if err != nil {
		return err
	}
	return axFailure(el.SetBool("AXSelected", true), "AXSelected")
}

// Ancestors walks AXParent up to the window, innermost first.
func (a *Actions) Ancestors(ctx context.Context, h view.Handle) ([]view.RawNode, error) {
	el, err := element(h)
	if err != nil {
		return nil, err
	}
	scale := ax.BackingScale()
	var out []view.RawNode
	cur := el
	for i := 0; i < view.MaxDepth; i++ {
		p, err := cur.Attr("AXParent")
		if err != nil {
			break
		}
		parent, ok := p.(*ax.Element)
		if !ok {
			break
		}
		v, err := parent.Attrs(nodeAttrs)
		if err != nil {
			break
		}
		role := str(v[aRole])
		if role == "AXApplication" {
			break
		}
		out = append(out, node(parent, v, scale, view.TreeOptions{}))
		if role == "AXWindow" || role == "AXSheet" {
			break
		}
		cur = parent
	}
	return out, nil
}

// Same compares accessibility elements.
func (a *Actions) Same(x, y view.Handle) bool {
	ex, err1 := element(x)
	ey, err2 := element(y)
	return err1 == nil && err2 == nil && ex.Equal(ey)
}

// raiseRetry bounds how long Raise retries an app that is still starting:
// a window that has just appeared can fail AXFrontmost or AXRaise (cannot
// complete, failure) for a moment.
const raiseRetry = 1500 * time.Millisecond

// Raise brings a window and its app to the front through accessibility.
func (a *Actions) Raise(ctx context.Context, w view.RawWindow) error {
	el, err := element(w.Handle)
	if err != nil {
		return err
	}
	app := ax.App(w.PID)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	deadline := time.Now().Add(raiseRetry)
	for {
		err := raiseOnce(app, el)
		if err == nil || !transientRaise(err) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func raiseOnce(app, el *ax.Element) error {
	if err := app.SetBool("AXFrontmost", true); err != nil {
		return axFailure(err, "AXFrontmost")
	}
	_ = el.SetBool("AXMain", true)
	// Some apps (Calculator) answer AXRaise with "attribute unsupported"
	// though the app came to the front; whether the window did is checked
	// by the caller.
	if err := el.Perform("AXRaise"); err != nil && !errors.Is(err, ax.ErrActionUnsupported) && !errors.Is(err, ax.ErrAttributeUnsupport) {
		return axFailure(err, "AXRaise")
	}
	return nil
}

// transientRaise says whether a raise failure can pass while the app
// finishes starting.
func transientRaise(err error) bool {
	var e *actions.AXError
	if !errors.As(err, &e) {
		return false
	}
	switch ax.Error(e.Code) {
	case ax.ErrCannotComplete, ax.ErrFailure:
		return true
	}
	return false
}

// CloseWindow presses the window's close button.
func (a *Actions) CloseWindow(ctx context.Context, w view.RawWindow) error {
	el, err := element(w.Handle)
	if err != nil {
		return err
	}
	v, err := el.Attr("AXCloseButton")
	if err != nil {
		return axFailure(err, "AXCloseButton")
	}
	btn, ok := v.(*ax.Element)
	if !ok {
		return errors.New("the window has no close button")
	}
	defer btn.Release()
	return axFailure(btn.Perform("AXPress"), "AXPress")
}

// ScreenLocked reads the login session's lock state.
func (a *Actions) ScreenLocked(context.Context) bool { return C.screenLocked() != 0 }

// Chromium says whether the process's app bundle carries a Chromium or
// Electron framework.
func (a *Actions) Chromium(pid int) bool {
	buf := make([]byte, 4096)
	n := C.pidPath(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n <= 0 {
		return false
	}
	path := string(buf[:n])
	i := strings.Index(path, ".app/")
	if i < 0 {
		return false
	}
	frameworks := filepath.Join(path[:i+4], "Contents", "Frameworks")
	entries, err := os.ReadDir(frameworks)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		for _, mark := range []string{"Electron Framework", "Chromium", "Chrome Framework", "Edge Framework", "Brave", "Vivaldi", "Opera Framework"} {
			if strings.Contains(name, mark) {
				return true
			}
		}
	}
	return false
}

// Scale is the main display's backing scale.
func (a *Actions) Scale() float64 { return ax.BackingScale() }

// LockedDelivery is true with the bundled driver, which takes locked_input.
func (a *Actions) LockedDelivery() bool { return a.Client != nil }

// Driver calls a cua-driver tool on the input lane.
func (a *Actions) Driver(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if a.Client == nil {
		return nil, driver.ErrNoDriver
	}
	return a.Client.Call(ctx, driver.LaneInput, tool, args)
}

// windowState is the part of get_window_state a hit-checked click needs.
type windowState struct {
	Elements []struct {
		Token string `json:"element_token"`
		Role  string `json:"role"`
		Label string `json:"label"`
		Title string `json:"title"`
		Frame *struct {
			X, Y, W, H float64
		} `json:"frame"`
	} `json:"elements"`
}

// HitClick finds the element in the driver's own snapshot of the window by
// its frame (and name, among equal frames) and asks for a verify_hit click
// on its token. An element the snapshot lacks is refused with nothing sent.
func (a *Actions) HitClick(ctx context.Context, w view.RawWindow, n view.RawNode, count int) (json.RawMessage, error) {
	if a.Client == nil {
		return nil, driver.ErrNoDriver
	}
	id, ok := cgWindowID(w.Key)
	if !ok || n.Bounds == nil {
		return nil, hitRefusal("the element has no window id or frame")
	}
	raw, err := a.Client.Call(ctx, driver.LaneInput, "get_window_state", map[string]any{
		"pid": w.PID, "window_id": id, "tree_format": "elements", "include_screenshot": false,
	})
	if err != nil {
		var de *driver.Error
		if errors.As(err, &de) && !de.Transport {
			return nil, hitRefusal("the driver could not read the window: " + de.Message)
		}
		return nil, err
	}
	var st windowState
	if err := json.Unmarshal(driver.Payload(raw), &st); err != nil {
		return nil, hitRefusal("the window state did not decode")
	}
	scale := ax.BackingScale()
	token, best := "", math.MaxFloat64
	for _, e := range st.Elements {
		if e.Token == "" || e.Frame == nil {
			continue
		}
		f := [4]float64{e.Frame.X * scale, e.Frame.Y * scale, e.Frame.W * scale, e.Frame.H * scale}
		d := 0.0
		for i := range f {
			d += math.Abs(f[i] - float64(n.Bounds[i]))
		}
		if d > 8 {
			continue
		}
		if e.Title == n.Name || e.Label == n.Name {
			d -= 0.5
		}
		if d < best {
			token, best = e.Token, d
		}
	}
	if token == "" {
		return nil, hitRefusal("the element is not in the driver's snapshot of the window")
	}
	args := map[string]any{"element_token": token, "verify_hit": true}
	if count > 1 {
		args["count"] = count
	}
	return a.Client.Call(ctx, driver.LaneInput, "click", args)
}

func cgWindowID(key string) (int, bool) {
	s, ok := strings.CutPrefix(key, "cg:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// hitRefusal is a hit-checked click refused before anything was sent.
func hitRefusal(why string) error {
	msg := fmt.Sprintf("click.verify_hit: %s; nothing was sent.", why)
	b, _ := json.Marshal(map[string]any{"status": "refused", "code": "hit_check_unavailable", "refusal": map[string]string{"code": "hit_check_unavailable", "message": msg}})
	return &driver.Error{Tool: "click", Message: msg, Result: b}
}

var _ actions.Backend = (*Actions)(nil)

// FocusElement sets AXFocused.
func (a *Actions) FocusElement(ctx context.Context, h view.Handle) error {
	el, err := element(h)
	if err != nil {
		return err
	}
	return axFailure(el.SetBool("AXFocused", true), "AXFocused")
}

// isInt says whether an attribute value is an integer number.
func isInt(v any) bool {
	_, ok := v.(int64)
	return ok
}
