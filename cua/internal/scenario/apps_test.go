//go:build scenario

package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/parity"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// ---------------------------------------------------------------------------
// Spaces

// CAP-RGB-034: background input lists windows on other Spaces as such.
func TestOtherSpaceWindowListed(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-034"},
		Scene:   "spaces",
		Config:  Config{Input: "background"},
		Timeout: 3 * time.Minute,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, perr, err := s.Observe(ctx, M{"scope": "desktop"})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			for _, w := range o.Windows {
				if w.Title == "Away Only" {
					if w.Space != "other" || w.Active {
						return parity.Outcome{Class: "space", Detail: fmt.Sprintf("%+v", w)}, nil
					}
					return parity.Outcome{OK: true}, nil
				}
			}
			return parity.Outcome{Class: "not_listed", Detail: "Away Only is not listed"}, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "windows", nil)
			if err != nil {
				return parity.Outcome{}, err
			}
			var ws []map[string]any
			_ = json.Unmarshal([]byte(d.Text), &ws)
			for _, w := range ws {
				if w["title"] == "Away Only" {
					if w["space"] != "other" {
						return parity.Outcome{Class: "space", Detail: fmt.Sprint(w)}, nil
					}
					return parity.Outcome{OK: true}, nil
				}
			}
			return parity.Outcome{Class: "not_listed", Detail: clip(d.Text, 300)}, nil
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-034: a title two windows of the app share across Spaces is
// refused.
func TestOtherSpaceTwinRefused(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-034", "CAP-RGB-045"},
		Scene:   "spaces",
		Config:  Config{Input: "background"},
		Timeout: 3 * time.Minute,
		Fennec:  fennecPlan(M{"type": "x", "window": "Space Twin", "any_focus": true}),
		Shuttle: shuttleDesk("type", M{"text": "x", "window": "Space Twin"}),
		Keys:    []string{"controls"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("space-field@space-here", r.State.Str("controls.space-field@space-here"), "")
			c.eq("space-field@space-away", r.State.Str("controls.space-field@space-away"), "")
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGA-054: with an allowlist, background typing reaches an allowed
// app's window on another Space.
func TestAllowlistOtherSpaceType(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-054"},
		Scene:   "spaces",
		Config:  Config{Input: "background", Allow: []string{"CUAFixture"}},
		Timeout: 3 * time.Minute,
		Fennec:  fennecPlan(M{"type": "away", "window": "Away Only", "any_focus": true}),
		Shuttle: shuttleDesk("type", M{"text": "away", "window": "Away Only", "focus": "unverified"}),
		Keys:    []string{"controls"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("space-field@away-only", r.State.Str("controls.space-field@away-only"), "away")
			return c.err()
		},
	})
}

// CAP-RGA-059: a window keeps its ref when it moves to another Space.
func TestWindowMovesSpace(t *testing.T) {
	run(t, Case{
		Caps:      []string{"CAP-RGA-059"},
		Scene:     "basic",
		Config:    Config{Input: "background"},
		Timeout:   3 * time.Minute,
		NoShuttle: "spec capability: fennec-cua's refs",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			ref := func() (string, error) {
				o, perr, err := s.Observe(ctx, M{"scope": "desktop"})
				if err != nil || perr != nil {
					return "", fmt.Errorf("observe: %v %v", err, perr)
				}
				for _, w := range o.Windows {
					if w.Title == "Basic" {
						return w.Ref + "/" + w.Space, nil
					}
				}
				return "", nil
			}
			before, err := ref()
			if err != nil {
				return parity.Outcome{}, err
			}
			w, err := window(ctx, "CUAFixture", "Basic")
			if err != nil {
				return parity.Outcome{}, err
			}
			el, ok := w.Handle.(*ax.Element)
			if !ok {
				return parity.Outcome{}, fmt.Errorf("no AX window")
			}
			if err := el.SetBool("AXFullScreen", true); err != nil {
				return parity.Outcome{}, err
			}
			defer func() { _ = el.SetBool("AXFullScreen", false); time.Sleep(1500 * time.Millisecond) }()
			if err := sleep(ctx, 2500); err != nil {
				return parity.Outcome{}, err
			}
			// Back on the desktop Space, the full-screen window is away.
			_ = openApp(ctx, "-a", "Finder")
			if err := sleep(ctx, 1500); err != nil {
				return parity.Outcome{}, err
			}
			after, err := ref()
			if err != nil {
				return parity.Outcome{}, err
			}
			b, a := strings.Split(before, "/")[0], strings.Split(after, "/")[0]
			if b == "" || a != b {
				return parity.Outcome{Class: "ref_changed", Detail: before + " -> " + after}, nil
			}
			return parity.Outcome{OK: true, Detail: before + " -> " + after}, nil
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// ---------------------------------------------------------------------------
// Real apps

// textEdit opens a new TextEdit document from a scratch file and returns
// its window title.
func textEdit(ctx context.Context, name, text string) (string, error) {
	quitApp(ctx, "TextEdit")
	p := scratch(name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return "", err
	}
	if err := openApp(ctx, "-a", "TextEdit", p); err != nil {
		return "", err
	}
	w, err := waitWindow(ctx, "TextEdit", name, 15*time.Second)
	if err != nil {
		return "", err
	}
	return w.Title, sleep(ctx, 500)
}

// CAP-RGB-052: the window list ranks the frontmost app's window first.
func TestWindowOrderAcrossApps(t *testing.T) {
	order := func(text string) parity.Outcome {
		te, fx := strings.Index(text, "order.txt"), strings.Index(text, `"Basic"`)
		if te < 0 || fx < 0 || te > fx {
			return parity.Outcome{Class: "order", Detail: clip(text, 400)}
		}
		return parity.Outcome{OK: true}
	}
	run(t, Case{
		Caps:    []string{"CAP-RGB-052"},
		Scene:   "basic",
		Setup:   func(ctx context.Context) error { _, err := textEdit(ctx, "order.txt", "front"); return err },
		Cleanup: func(ctx context.Context) { quitApp(ctx, "TextEdit") },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			r, err := s.Tool(ctx, protocol.ToolObserve, M{"scope": "desktop"})
			if err != nil {
				return parity.Outcome{}, err
			}
			return order(strings.SplitN(r.Text(), `"windows"`, 2)[len(strings.SplitN(r.Text(), `"windows"`, 2))-1]), nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "windows", nil)
			if err != nil {
				return parity.Outcome{}, err
			}
			return order(d.Text), nil
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// menuTitle reads the title of a menu entry at a path in an app's menus.
func menuEntryNamed(ctx context.Context, app, title, name string) bool {
	w, err := window(ctx, app, title)
	if err != nil {
		return false
	}
	t, err := dk.Tree(ctx, w, view.TreeOptions{Menus: true})
	if err != nil {
		return false
	}
	for _, n := range t.Nodes {
		if strings.HasPrefix(n.Role, "menu") && n.Name == name {
			return true
		}
	}
	return false
}

// CAP-RGA-033: TextEdit's Wrap to Page, used once, is matched as Wrap to
// Window though its title read stale.
func TestTextEditRetitledMenuEntry(t *testing.T) {
	var title string
	run(t, Case{
		Caps:    []string{"CAP-RGA-033"},
		Setup:   func(ctx context.Context) (err error) { title, err = textEdit(ctx, "wrap.txt", "wrap me"); return err },
		Cleanup: func(ctx context.Context) { quitApp(ctx, "TextEdit") },
		Fennec:  chain(fennecPlan(M{"menu": "Format > Wrap to Page"}), fennecPlan(M{"menu": "Format > Wrap to Window"})),
		Shuttle: chain(shuttlePlan(M{"menu": "Format > Wrap to Page"}), shuttlePlan(M{"menu": "Format > Wrap to Window"})),
		Keys:    []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			_ = title
			return c.err()
		},
	})
}

// CAP-RGB-075: TextEdit's replace confirmation on its save sheet is listed
// as its own dialog, and a plan saving under a taken name does not end ok.
func TestTextEditReplaceAlertOnSheet(t *testing.T) {
	taken := scratch("taken-te.txt")
	run(t, Case{
		Caps: []string{"CAP-RGB-075"},
		Setup: func(ctx context.Context) error {
			if err := os.WriteFile(taken, []byte("keep"), 0o644); err != nil {
				return err
			}
			quitApp(ctx, "TextEdit")
			if err := openApp(ctx, "-a", "TextEdit"); err != nil {
				return err
			}
			_, err := waitWindow(ctx, "TextEdit", "", 15*time.Second)
			return err
		},
		Cleanup: func(ctx context.Context) { quitApp(ctx, "TextEdit") },
		Fennec:  fennecPlan(M{"key": "cmd+n"}, M{"type": "new text"}, M{"key": "cmd+s"}, M{"type": taken + "\n"}),
		Shuttle: shuttlePlan(M{"key": "cmd+n"}, M{"type": "new text"}, M{"key": "cmd+s"}, M{"type": taken + "\n"}),
		Keys:    []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			b, _ := os.ReadFile(taken)
			c.eq("the taken file", string(b), "keep")
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-054: a foreground point click in System Settings' sidebar
// switches its pane.
func TestSystemSettingsSidebarClick(t *testing.T) {
	var x, y int
	setup := func(ctx context.Context) error {
		quitApp(ctx, "System Settings")
		if err := openApp(ctx, "-a", "System Settings"); err != nil {
			return err
		}
		w, err := waitWindow(ctx, "System Settings", "", 20*time.Second)
		if err != nil {
			return err
		}
		if err := sleep(ctx, 2000); err != nil {
			return err
		}
		nodes, err := tree(ctx, w)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if n.Bounds != nil && (n.Name == "Appearance" || (n.Value != nil && *n.Value == "Appearance")) {
				x, y = center(*n.Bounds)
				return nil
			}
		}
		return fmt.Errorf("no Appearance in the sidebar")
	}
	shows := func(ctx context.Context) bool {
		w, err := window(ctx, "System Settings", "")
		return err == nil && strings.Contains(w.Title, "Appearance")
	}
	run(t, Case{
		Caps:    []string{"CAP-RGB-054"},
		Setup:   setup,
		Timeout: 3 * time.Minute,
		Cleanup: func(ctx context.Context) { quitApp(ctx, "System Settings") },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(x, y)})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttleDesk("click", M{"x": x, "y": y})(ctx, s)
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			deadline := time.Now().Add(3 * time.Second)
			for !shows(ctx) && time.Now().Before(deadline) {
				_ = sleep(ctx, 200)
			}
			c.that(shows(ctx), "System Settings did not switch to Appearance")
			return c.err()
		},
	})
}

// CAP-RGA-018: input aimed at a macOS authorization prompt is refused with
// nothing sent.
func TestSecurityPromptRefusal(t *testing.T) {
	var cmd *exec.Cmd
	var x, y int
	setup := func(ctx context.Context) error {
		cmd = exec.Command("osascript", "-e", `do shell script "true" with administrator privileges`)
		if err := cmd.Start(); err != nil {
			return err
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			sw, _ := ax.ServerWindows(true)
			for _, w := range sw {
				if (w.Owner == "SecurityAgent" || w.Owner == "universalAccessAuthWarn") && w.Bounds.W > 100 {
					s := ax.BackingScale()
					x, y = int((w.Bounds.X+w.Bounds.W/2)*s), int((w.Bounds.Y+w.Bounds.H-30)*s)
					return sleep(ctx, 500)
				}
			}
			_ = sleep(ctx, 200)
		}
		return fmt.Errorf("no authorization prompt showed")
	}
	cleanup := func(ctx context.Context) {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_, _ = sh(ctx, "sudo pkill -x SecurityAgent")
	}
	run(t, Case{
		Caps:    []string{"CAP-RGA-018"},
		Setup:   setup,
		Cleanup: cleanup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"click": pt(x, y)})
			if err != nil {
				return parity.Outcome{}, err
			}
			if a.OK() {
				return parity.Outcome{Class: "sent", Detail: "a click went to the prompt"}, nil
			}
			return parity.Outcome{Class: a.Code(), Detail: a.Message()}, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "click", M{"x": x, "y": y})
			if err != nil {
				return parity.Outcome{}, err
			}
			if !d.IsError {
				return parity.Outcome{Class: "sent", Detail: "a click went to the prompt"}, nil
			}
			return parity.Outcome{Class: "security_prompt", Detail: clip(d.Text, 300)}, nil
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("class", r.Outcome.Class, string(protocol.ErrSecurityPrompt))
			return c.err()
		},
	})
}
