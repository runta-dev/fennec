package engine

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"fennec/cua/internal/protocol"
)

// AppLister lists the installed and running apps (the driver's list_apps
// in production); nil leaves the inventory to the apps with windows.
type AppLister func(ctx context.Context) ([]protocol.AppInfo, error)

// AppsTool answers the app inventory the REPL's cua.getState, listApps and
// getApp read: the lister's apps, marked running when they own an admitted
// window, after every app with an admitted window the lister missed. It
// reads only; the windows come from observe, so the allowlist applies.
func (e *Engine) AppsTool(list AppLister) Tool {
	return func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error) {
		var req protocol.AppsRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		out := protocol.AppsResult{Apps: []protocol.AppInfo{}}
		if obs := e.Impl[protocol.ToolObserve]; obs != nil {
			v, perr := obs(ctx, call, json.RawMessage(`{"scope":"desktop"}`))
			if perr != nil {
				out.Errors = append(out.Errors, "windows: "+perr.Message)
			} else if res, ok := v.(*protocol.ObserveResult); ok && res != nil {
				out.Windows = res.Windows
			}
		}
		if list != nil {
			apps, err := list(ctx)
			if err != nil {
				out.Errors = append(out.Errors, "apps: "+err.Error())
			}
			out.Apps = append(out.Apps, apps...)
		}
		seen := map[string]int{}
		for i, a := range out.Apps {
			seen[strings.ToLower(a.Name)] = i
		}
		// Apps the lister missed but that show a window come first, in the
		// windows' front-to-back order: they are processes that are not
		// installed apps yet put something on screen (a dialog osascript
		// raised, Spotlight), which an agent must notice before the long
		// list of installed apps.
		var extra []protocol.AppInfo
		for _, w := range out.Windows {
			if w.App == "" {
				continue
			}
			if i, ok := seen[strings.ToLower(w.App)]; ok {
				if i >= 0 {
					out.Apps[i].Running = true
					if out.Apps[i].PID == 0 {
						out.Apps[i].PID = w.PID
					}
				}
				continue
			}
			seen[strings.ToLower(w.App)] = -1
			extra = append(extra, protocol.AppInfo{Name: w.App, PID: w.PID, Running: true})
		}
		if len(extra) > 0 {
			out.Apps = append(extra, out.Apps...)
		}
		return out, nil
	}
}

// Pasteboard reads and writes the general pasteboard's text.
type Pasteboard interface {
	Text(ctx context.Context) (string, error)
	SetText(ctx context.Context, text string) error
}

// PasteRequest is the REPL's paste: the text, its format, and the window
// whose focused element receives it.
type PasteRequest struct {
	Text   string `json:"text"`
	Format string `json:"format,omitempty"`
	Window string `json:"window,omitempty"`
}

// PasteSettle is how long the pasted text stays on the pasteboard before
// the previous text is put back, so the app reads it first.
var PasteSettle = 300 * time.Millisecond

// PasteTool puts text on the pasteboard, sends Command-V through act (so
// the input policy applies) and restores the previous pasteboard text. It
// answers act's result.
func (e *Engine) PasteTool(pb Pasteboard) Tool {
	return func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error) {
		var req PasteRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		if pb == nil {
			return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "the pasteboard is not available in this build; nothing was pasted", Hint: "use typeText"}
		}
		act := e.Impl[protocol.ToolAct]
		if act == nil {
			return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "act is not available in this build; nothing was pasted"}
		}
		release, err := call.Turn(ctx)
		if err != nil {
			return nil, TurnError(err)
		}
		defer release()
		old, readErr := pb.Text(ctx)
		if err := pb.SetText(ctx, req.Text); err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInternal, Message: "writing the pasteboard failed: " + err.Error() + "; nothing was pasted"}
		}
		step, _ := json.Marshal(map[string]any{"window": req.Window, "steps": []any{map[string]any{"key": "cmd+v"}}})
		out, perr := act(ctx, call, step)
		if readErr == nil {
			select {
			case <-time.After(PasteSettle):
			case <-ctx.Done():
			}
			_ = pb.SetText(context.WithoutCancel(ctx), old)
		}
		return out, perr
	}
}
