//go:build scenario

package scenario

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/parity"
	"fennec/cua/internal/protocol"
)

// The page Safari scenarios load: its title reports what the page saw.
const page = `<!doctype html><html><head><title>Ready</title></head><body>
<form id="login" onsubmit="document.title='Submitted '+document.getElementById('pw').value.length; return false">
<input id="user" aria-label="User"> <input id="pw" type="password" aria-label="Password"> <button type="submit">Go</button>
</form>
<textarea id="notes" aria-label="Notes" rows="4" cols="40"></textarea>
<button id="act" onclick="document.title='Activation '+(navigator.userActivation?navigator.userActivation.isActive:'none')+' '+event.isTrusted">Activate</button>
<script>document.getElementById('notes').addEventListener('input', function(e){ document.title = 'Notes ' + e.target.value })</script>
</body></html>`

func pageURL() string { return "file://" + scratchDir + "/page.html" }

func writePage() error { return os.WriteFile(scratchDir+"/page.html", []byte(page), 0o644) }

// safariTitle is the title of Safari's front window, or "".
func safariTitle(ctx context.Context) string {
	ws, err := windows(ctx)
	if err != nil {
		return ""
	}
	for _, w := range ws {
		if w.App == "Safari" && w.Role == "window" {
			return w.Title
		}
	}
	return ""
}

// waitTitle waits for Safari's window to carry a title holding part.
func waitTitle(ctx context.Context, part string, d time.Duration) (string, bool) {
	deadline := time.Now().Add(d)
	for {
		t := safariTitle(ctx)
		if strings.Contains(t, part) {
			return t, true
		}
		if time.Now().After(deadline) || sleep(ctx, 150) != nil {
			return t, false
		}
	}
}

// openPage starts Safari on the page and waits for it.
func openPage(ctx context.Context) error {
	if err := writePage(); err != nil {
		return err
	}
	if err := openApp(ctx, "-a", "Safari", pageURL()); err != nil {
		return err
	}
	if _, ok := waitTitle(ctx, "Ready", 20*time.Second); !ok {
		return fmt.Errorf("Safari did not show the page (front title %q)", safariTitle(ctx))
	}
	return sleep(ctx, 500)
}

func safariProcesses() int {
	out, _ := exec.Command("pgrep", "-x", "Safari").Output()
	return len(strings.Fields(string(out)))
}

func safariCase(c Case) Case {
	if c.Cleanup == nil {
		c.Cleanup = func(ctx context.Context) { quitApp(ctx, "Safari") }
	}
	if c.Keys == nil {
		c.Keys = []string{}
	}
	if c.Timeout == 0 {
		c.Timeout = 3 * time.Minute
	}
	return c
}

// titleCheck passes when Safari's title comes to hold part.
func titleCheck(part string, needOK bool) func(context.Context, *Result) error {
	return func(ctx context.Context, r *Result) error {
		var c checks
		t, ok := waitTitle(ctx, part, 4*time.Second)
		c.that(ok, "Safari's title is %q, want it to hold %q", t, part)
		if needOK {
			c.ok(r)
		}
		return c.err()
	}
}

// CAP-RGA-014: a cold launch of Safari with a URL shows the page in front.
func TestLaunchSafariWithURLCold(t *testing.T) {
	run(t, safariCase(Case{
		Caps:  []string{"CAP-RGA-014"},
		Setup: func(ctx context.Context) error { quitApp(ctx, "Safari"); return writePage() },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"launch": "Safari", "url": pageURL()})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttlePlan(M{"app": "Safari", "url": pageURL()})(ctx, s)
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			t, ok := waitTitle(ctx, "Ready", 5*time.Second)
			c.that(ok, "Safari shows %q", t)
			c.that(frontApp() == "Safari", "Safari is not in front: %s", frontApp())
			c.eq("Safari processes", safariProcesses(), 1)
			return c.err()
		},
	}))
}

// CAP-RGA-014: a launch with a URL of a running Safari behind Finder hands
// the URL to it and fronts it.
func TestLaunchSafariWithURLRunning(t *testing.T) {
	run(t, safariCase(Case{
		Caps: []string{"CAP-RGA-014"},
		Setup: func(ctx context.Context) error {
			if err := openPage(ctx); err != nil {
				return err
			}
			if err := os.WriteFile(scratchDir+"/other.html", []byte("<title>Other</title>other"), 0o644); err != nil {
				return err
			}
			return openApp(ctx, "-a", "Finder")
		},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"launch": "Safari", "url": "file://" + scratchDir + "/other.html"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttlePlan(M{"app": "Safari", "url": "file://" + scratchDir + "/other.html"})(ctx, s)
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			t, ok := waitTitle(ctx, "Other", 5*time.Second)
			c.that(ok, "Safari shows %q", t)
			c.that(frontApp() == "Safari", "Safari is not in front: %s", frontApp())
			c.eq("Safari processes", safariProcesses(), 1)
			return c.err()
		},
	}))
}

// CAP-RGA-015: a click on a Safari text area focuses it for the typing
// after.
func TestSafariTextAreaClickThenType(t *testing.T) {
	run(t, safariCase(Case{
		Caps:    []string{"CAP-RGA-015"},
		Setup:   openPage,
		Fennec:  fennecPlan(M{"click": `textbox "Notes"`}, M{"type": "hello"}),
		Shuttle: shuttlePlan(M{"click": "Notes"}, M{"type": "hello"}),
		Check:   titleCheck("Notes hello", true),
	}))
}

// CAP-RGA-003: a password into a Safari password field gets its one
// Return without a false stop.
func TestSafariPasswordReturn(t *testing.T) {
	run(t, safariCase(Case{
		Caps:    []string{"CAP-RGA-003"},
		Setup:   openPage,
		Fennec:  fennecPlan(M{"click": `textbox "Password"`}, M{"type": "pw-9\n", "secret": true}),
		Shuttle: shuttlePlan(M{"click": "Password"}, M{"type": "pw-9\n", "focus": M{"secure": true}}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			t, ok := waitTitle(ctx, "Submitted", 4*time.Second)
			c.that(ok, "the form was not submitted: %q", t)
			c.eq("title", t, "Submitted 4")
			c.noSecret(r, "pw-9")
			c.ok(r)
			return c.err()
		},
	}))
}

// CAP-RGB-053: a press on a web button counts as the user's activation.
func TestPressWebUserActivation(t *testing.T) {
	run(t, safariCase(Case{
		Caps:    []string{"CAP-RGB-053"},
		Setup:   openPage,
		Fennec:  fennecPlan(M{"press": `button "Activate"`}),
		Shuttle: shuttlePlan(M{"click": "Activate"}),
		Check:   titleCheck("Activation true", true),
	}))
}

// CAP-RGA-019: a background Return into Safari's address bar may not
// navigate; the result must not claim it did.
func TestSafariAddressBarBackgroundReturn(t *testing.T) {
	other := "file://" + scratchDir + "/other.html"
	claimsDone := func(o parity.Outcome) bool { return o.OK }
	run(t, safariCase(Case{
		Caps:   []string{"CAP-RGA-019"},
		Config: Config{Input: "background"},
		Setup: func(ctx context.Context) error {
			if err := os.WriteFile(scratchDir+"/other.html", []byte("<title>Other</title>other"), 0o644); err != nil {
				return err
			}
			if err := openPage(ctx); err != nil {
				return err
			}
			return openApp(ctx, "-a", "Finder")
		},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"key": "cmd+l", "window": "Ready"}, M{"type": other + "\n", "window": "Ready", "any_focus": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			if a.OK() && a.Step(1).Status != protocol.StatusConfirmed {
				o.Detail = "unconfirmed: " + a.Step(1).Evidence
			}
			return o, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return chain(shuttleDesk("key", M{"keys": "cmd+l", "window": "Ready"}), shuttleDesk("type", M{"text": other + "\n", "window": "Ready", "focus": "unverified"}))(ctx, s)
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			_, navigated := waitTitle(ctx, "Other", 3*time.Second)
			if !navigated {
				c.that(!claimsDone(r.Outcome) || strings.HasPrefix(r.Outcome.Detail, "unconfirmed"), "the result claims a navigation that did not happen")
			}
			return c.err()
		},
	}))
}
