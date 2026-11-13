// Package actions executes one act step (press, set, click, type, key,
// scroll, drag, menu, focus, close, launch) against a Backend and answers a
// protocol.StepResult: confirmed when the effect was read back, unconfirmed
// when it was sent but not proven (retry false), refused when nothing was
// sent. It is platform-free; internal/desk implements the Backend on macOS.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fennec/cua/internal/driver"
	"fennec/cua/internal/view"
)

// Backend is the desktop an Executor acts on: an accessibility side (reads
// through view.Source, element actions and values) and a driver side (the
// cua-driver tools).
type Backend interface {
	view.Source

	// Perform runs an accessibility action such as AXPress on an element.
	// A failure the app reports carries an AXError.
	Perform(ctx context.Context, h view.Handle, action string) error
	// ActionNames lists an element's raw accessibility action names.
	ActionNames(ctx context.Context, h view.Handle) ([]string, error)
	// SelectRange sets a text element's selected range (AXSelectedTextRange).
	SelectRange(ctx context.Context, h view.Handle, loc, length int) error
	// SetValue writes an element's value.
	SetValue(ctx context.Context, h view.Handle, value string) error
	// Select selects a tab, option, row or tree item through accessibility.
	Select(ctx context.Context, h view.Handle) error
	// FocusElement gives an element the keyboard focus (AXFocused).
	FocusElement(ctx context.Context, h view.Handle) error
	// Ancestors are an element's ancestors, innermost first, up to its window.
	Ancestors(ctx context.Context, h view.Handle) ([]view.RawNode, error)
	// Same says whether two handles are the same element.
	Same(a, b view.Handle) bool
	// Raise brings a window to the front and its app with it.
	Raise(ctx context.Context, w view.RawWindow) error
	// CloseWindow presses a window's close button.
	CloseWindow(ctx context.Context, w view.RawWindow) error
	// ScreenLocked says whether the screen is locked or shows the screen saver.
	ScreenLocked(ctx context.Context) bool
	// Chromium says whether a process renders with Chromium (Chrome, Edge,
	// Electron apps), which needs paced background typing.
	Chromium(pid int) bool

	// Driver calls a cua-driver tool on the input lane. It answers
	// driver.ErrNoDriver when no driver is bundled.
	Driver(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error)
	// HitClick asks the driver for a hit-checked click (verify_hit) on an
	// element of window w: it fronts the window and clicks the element's
	// centre only if a hit test there finds the element or something inside it.
	HitClick(ctx context.Context, w view.RawWindow, n view.RawNode, count int) (json.RawMessage, error)
	// LockedDelivery says whether the driver can send keys through a locked screen.
	LockedDelivery() bool
	// Scale is the main display's desktop pixels per point.
	Scale() float64
}

// AXError is an accessibility failure with its AXError code.
type AXError struct {
	Code int
	Op   string
}

func (e *AXError) Error() string { return fmt.Sprintf("%s: accessibility error %d", e.Op, e.Code) }

// AXError codes that change what happens next.
const (
	AXCannotComplete     = -25204 // the app did not answer in time
	AXActionUnsupported  = -25206
	AXAttributeUnsupport = -25205
)

// axCode is the AXError code of err, or 0.
func axCode(err error) int {
	var ae *AXError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

// Reply is the part of a driver's structured reply an action reads.
type Reply struct {
	Effect  string `json:"effect"` // confirmed, partial, unverifiable, suspected_noop, refused
	Summary string `json:"summary"`
	Hint    string `json:"hint"`
	Note    string `json:"note"`
	Code    string `json:"code"`
	Status  string `json:"status"`
	Refusal *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"refusal"`
	Error *struct {
		Code string `json:"code"`
		Hint string `json:"hint"`
	} `json:"error"`
	Escalation *struct {
		Recommended string `json:"recommended"`
		Reason      string `json:"reason"`
	} `json:"escalation"`
	// Typing.
	Characters     int  `json:"characters"`
	DeliveredChars *int `json:"delivered_chars"`
	SkippedChars   *int `json:"skipped_chars"`
	Terminal       bool `json:"terminal"`
	LockedInput    bool `json:"locked_input"`
	// Focused is the keyboard focus after a background click.
	Focused *Focus `json:"focused"`
}

// Focus is the element with the keyboard focus after a click.
type Focus struct {
	Role    string `json:"role"`
	Name    string `json:"name"`
	AtPoint bool   `json:"at_point"`
	Text    bool   `json:"text_field"`
}

// RefusalCode is the refusal's code, when the driver refused.
func (r Reply) RefusalCode() string {
	switch {
	case r.Refusal != nil:
		return r.Refusal.Code
	case r.Error != nil && r.Effect == "refused":
		return r.Error.Code
	case r.Effect == "refused" || r.Status == "refused":
		return r.Code
	}
	return ""
}

// Advice is the driver's advice on an unconfirmed result.
func (r Reply) Advice() string {
	parts := []string{}
	for _, s := range []string{r.Note, r.Hint} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if r.Escalation != nil && r.Escalation.Reason != "" {
		parts = append(parts, "the driver suggests "+r.Escalation.Recommended+": "+r.Escalation.Reason)
	}
	return strings.Join(parts, " ")
}

func parseReply(raw json.RawMessage) Reply {
	var r Reply
	_ = json.Unmarshal(driver.Payload(raw), &r)
	return r
}

// call is the outcome of one driver call.
type call struct {
	reply Reply
	err   error
	// sent is false only when the driver refused before sending anything.
	sent bool
	msg  string
}

// driverCall runs a driver tool and classifies the outcome: a refusal the
// driver states sent nothing; a transport failure after writing, a timeout
// or any other failure may have landed and is never resent.
func driverCall(ctx context.Context, b Backend, tool string, args map[string]any) call {
	raw, err := b.Driver(ctx, tool, args)
	c := classify(raw, err)
	if !c.sent && c.reply.Code == contextMissing && args["window_id"] != nil {
		// Input aimed at a window's own pixels needs the driver to have
		// read that window in this session; it refused before sending,
		// so read the window and send once more.
		if _, rerr := b.Driver(ctx, "get_window_state", map[string]any{"pid": args["pid"], "window_id": args["window_id"]}); rerr == nil {
			raw, err = b.Driver(ctx, tool, args)
			c = classify(raw, err)
		}
	}
	return c
}

// contextMissing is the driver's refusal of window-pixel input for a window
// it has not read in this session; nothing was sent.
const contextMissing = "screenshot_context_missing"

func classify(raw json.RawMessage, err error) call {
	if err == nil && isErrorReply(raw) {
		// A result the driver marks as an error failed, whatever its
		// fields say; it is classified like a returned error.
		err = &driver.Error{Message: replyText(raw), Result: raw}
	}
	if err == nil {
		r := parseReply(raw)
		c := call{reply: r, sent: true}
		if r.RefusalCode() != "" {
			c.sent = false
			c.msg = refusalMessage(r)
			c.err = errors.New(c.msg)
		}
		return c
	}
	c := call{err: err, sent: true, msg: err.Error()}
	if errors.Is(err, driver.ErrNoDriver) {
		c.sent = false
		return c
	}
	var de *driver.Error
	if errors.As(err, &de) {
		if len(de.Result) > 0 {
			c.reply = parseReply(de.Result)
		}
		switch {
		case c.reply.RefusalCode() != "":
			c.sent = false
		case de.Transport && !strings.Contains(de.Message, "may have been sent") && !strings.HasPrefix(de.Message, "no reply"):
			// Connecting or writing failed: the request never left.
			c.sent = false
		case c.reply.Code == contextMissing:
			c.sent = false
		case strings.Contains(de.Message, "nothing was sent") || strings.Contains(de.Message, "No fallback input was sent"):
			c.sent = false
		}
	}
	return c
}

func refusalMessage(r Reply) string {
	switch {
	case r.Refusal != nil && r.Refusal.Message != "":
		return r.Refusal.Message
	case r.Summary != "":
		return r.Summary
	}
	return "refused: " + r.RefusalCode()
}

// isErrorReply says whether a driver result is marked isError.
func isErrorReply(raw json.RawMessage) bool {
	var r struct {
		IsError bool `json:"isError"`
	}
	return json.Unmarshal(raw, &r) == nil && r.IsError
}

// replyText joins a driver result's text blocks.
func replyText(raw json.RawMessage) string {
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &r)
	var parts []string
	for _, c := range r.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 {
		return "the driver answered an error without a reason"
	}
	return strings.Join(parts, "\n")
}
