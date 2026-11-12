//go:build scenario

package scenario

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/desk"
	"fennec/cua/internal/parity"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// term runs a bash script in a new Terminal window titled SCN-<name> and
// waits for that window to be in front. Its output file is out.
type term struct {
	name  string
	body  string // bash; OUT names the output file
	out   string
	title string // the window's whole title once open
}

func newTerm(name, body string) *term {
	return &term{name: name, body: body, out: scratch(name + ".out")}
}

func (tm *term) setup(ctx context.Context) error {
	_ = os.Remove(tm.out)
	tm.close(ctx)
	script := scratch(tm.name + ".command")
	text := fmt.Sprintf("#!/bin/bash\nprintf '\\033]0;SCN-%s\\007'\ntty > %q\nOUT=%q\nclear\n%s\nsleep 600\n", tm.name, tm.out+".tty", tm.out, tm.body)
	if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
		return err
	}
	if err := openApp(ctx, "-a", "Terminal", script); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ws, err := windows(ctx)
		if err == nil {
			for _, w := range ws {
				if w.App == "Terminal" && strings.Contains(w.Title, "SCN-"+tm.name) && w.Active {
					tm.title = w.Title
					// Let the prompt show.
					return sleep(ctx, 800)
				}
			}
		}
		if err := sleep(ctx, 200); err != nil {
			return err
		}
	}
	return fmt.Errorf("no Terminal window SCN-%s in front", tm.name)
}

// close ends the script and closes its window.
func (tm *term) close(ctx context.Context) {
	_, _ = sh(ctx, "pkill -f "+quoteAll([]string{tm.name + ".command"})[0])
	if b, err := os.ReadFile(tm.out + ".tty"); err == nil {
		// Everything on its terminal, an exec'd shell included.
		_, _ = sh(ctx, "pkill -9 -t "+strings.TrimPrefix(strings.TrimSpace(string(b)), "/dev/"))
	}
	time.Sleep(300 * time.Millisecond)
	ws, err := windows(ctx)
	if err != nil {
		return
	}
	act := desk.NewActions(dk, nil)
	for _, w := range ws {
		if w.App == "Terminal" && strings.Contains(w.Title, "SCN-"+tm.name) {
			_ = act.CloseWindow(ctx, w)
		}
	}
}

// output waits briefly for the script's output file.
func (tm *term) output(ctx context.Context, d time.Duration) (string, bool) {
	return waitFile(ctx, tm.out, d)
}

// termCase builds a Terminal scenario.
func termCase(tm *term, c Case) Case {
	c.Setup = tm.setup
	c.Cleanup = tm.close
	if c.Keys == nil {
		c.Keys = []string{}
	}
	return c
}

// noSecret fails when a password shows in a result.
func (c *checks) noSecret(r *Result, secret string) {
	for _, t := range r.Texts {
		c.that(!strings.Contains(resultText(t), secret), "the password shows in a result")
	}
}

// CAP-RGA-003: a read -s prompt gets the password and one Return.
func TestTerminalSecretReadS(t *testing.T) {
	tm := newTerm("reads", `printf 'Password: '; IFS= read -rs pw; printf '%s' "$pw" > "$OUT"; echo; echo DONE`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-003"},
		Fennec:  fennecPlan(M{"type": "s3cret-1\n", "secret": true}),
		Shuttle: shuttlePlan(M{"type": "s3cret-1\n", "secret": true}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			got, ok := tm.output(ctx, 3*time.Second)
			c.that(ok, "the prompt never got its Return")
			c.eq("password read", got, "s3cret-1")
			c.noSecret(r, "s3cret-1")
			c.ok(r)
			return c.err()
		},
	}))
}

// CAP-RGA-003: a prompt that echoes stops the step before its Return.
func TestTerminalSecretEchoOn(t *testing.T) {
	tm := newTerm("echoon", `printf 'Password: '; IFS= read -r pw; printf '%s' "$pw" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-003"},
		Fennec:  fennecPlan(M{"type": "s3cret-2\n", "secret": true}),
		Shuttle: shuttlePlan(M{"type": "s3cret-2\n", "secret": true}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			_, ok := tm.output(ctx, 2*time.Second)
			c.that(!ok, "Return went in after the password showed on screen")
			c.noSecret(r, "s3cret-2")
			c.stopped(r)
			return c.err()
		},
	}))
}

// CAP-RGA-008: text typed at a read -s prompt arrives once.
func TestTerminalReadSOnce(t *testing.T) {
	tm := newTerm("readsonce", `IFS= read -rs x; printf '%s' "$x" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-008"},
		Fennec:  fennecPlan(M{"type": "abc"}, M{"key": "return"}),
		Shuttle: shuttlePlan(M{"type": "abc"}, M{"key": "return"}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			got, ok := tm.output(ctx, 3*time.Second)
			c.that(ok, "the program never read its line")
			c.eq("read", got, "abc")
			return c.err()
		},
	}))
}

// CAP-RGA-008: under auto input an unconfirmed terminal type is not
// retyped in the foreground.
func TestAutoModeTerminalNoForegroundRetype(t *testing.T) {
	tm := newTerm("autoreads", `IFS= read -rs x; printf '%s' "$x" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:   []string{"CAP-RGA-008", "CAP-RGB-032"},
		Config: Config{Input: "auto"},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"type": "abc", "window": tm.title}, M{"key": "return", "window": tm.title})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return chain(shuttleDesk("type", M{"text": "abc", "window": tm.title}), shuttleDesk("key", M{"keys": "return", "window": tm.title}))(ctx, s)
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			got, ok := tm.output(ctx, 3*time.Second)
			c.that(ok, "the program never read its line")
			c.eq("read", got, "abc")
			return c.err()
		},
	}))
}

// CAP-RGA-006: a command line and then a read -s password in one type
// step are each submitted once.
func TestTerminalReadSThenPassword(t *testing.T) {
	tm := newTerm("readsthen", `IFS= read -r cmd; IFS= read -rs pw; printf '%s|%s' "$cmd" "$pw" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-006"},
		Fennec:  fennecPlan(M{"type": "first\nsecond\n"}),
		Shuttle: shuttlePlan(M{"type": "first\nsecond\n"}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			got, ok := tm.output(ctx, 3*time.Second)
			c.that(ok, "the program never read both lines")
			c.eq("read", got, "first|second")
			return c.err()
		},
	}))
}

// CAP-RGA-004: a trailing space the terminal may trim still confirms.
func TestTerminalTrailingSpace(t *testing.T) {
	tm := newTerm("trailing", `exec bash --norc --noprofile`)
	run(t, termCase(tm, Case{
		Caps: []string{"CAP-RGA-004"},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"type": "echo trailing "})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			if a.OK() && a.Step(0).Status != protocol.StatusConfirmed {
				o.OK, o.Class, o.Detail = false, "unconfirmed", a.Step(0).Evidence
			}
			return o, nil
		},
		Shuttle: shuttlePlan(M{"type": "echo trailing "}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	}))
}

// CAP-RGA-005: a screen that shows part of the typed text stops the plan
// before Return.
func TestTerminalPartialType(t *testing.T) {
	tm := newTerm("partial", `stty -echo; IFS= read -r -n 3 a; printf '%s' "$a"; IFS= read -r rest; printf '%s%s' "$a" "$rest" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-005"},
		Fennec:  fennecPlan(M{"type": "abcdef\n"}),
		Shuttle: shuttlePlan(M{"type": "abcdef\n"}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			_, ok := tm.output(ctx, 2*time.Second)
			c.that(!ok, "Return went in after only part of the line showed")
			c.stopped(r)
			return c.err()
		},
	}))
}

// CAP-RGA-011: a command already in scrollback that shows only in part is
// not confirmed.
func TestTerminalRepeatCommandPartial(t *testing.T) {
	tm := newTerm("repeat", `echo 'git push'; stty -echo; IFS= read -r -n 6 a; printf '%s' "$a"; IFS= read -r rest; printf '%s%s' "$a" "$rest" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-011"},
		Fennec:  fennecPlan(M{"type": "git push\n"}),
		Shuttle: shuttlePlan(M{"type": "git push\n"}),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			_, ok := tm.output(ctx, 2*time.Second)
			c.that(!ok, "Return went in although only 'git pu' showed")
			c.stopped(r)
			return c.err()
		},
	}))
}

// CAP-RGA-012: 620 characters go into a background Terminal in batches.
func TestBackgroundTerminalTypeThroughput(t *testing.T) {
	text := strings.Repeat("abcdefghij", 62)
	tm := newTerm("throughput", `IFS= read -r line; printf '%s' "$line" > "$OUT"`)
	run(t, termCase(tm, Case{
		Caps:      []string{"CAP-RGA-012"},
		Config:    Config{Input: "background"},
		NoShuttle: "spec capability: fennec-cua only",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"type": text, "window": tm.title, "any_focus": true}, M{"key": "return", "window": tm.title})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			o.Detail = fmt.Sprintf("type took %d ms; %s", a.Step(0).Ms, o.Detail)
			if a.Step(0).Ms > 5000 {
				o.OK, o.Class = false, "slow"
			}
			return o, nil
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			got, ok := tm.output(ctx, 3*time.Second)
			c.that(ok, "the line never arrived")
			c.eq("characters read", len(got), len(text))
			c.that(r.Outcome.OK || r.Outcome.Class != "slow", "%s", r.Outcome.Detail)
			return c.err()
		},
	}))
}

// CAP-RGA-055: a tail read of a long Terminal shows its last lines and
// says the text was cut.
func TestTerminalTailRead(t *testing.T) {
	tm := newTerm("tail", `seq -f 'line %g' 1 9000; echo TAIL-MARK`)
	run(t, termCase(tm, Case{
		Caps:    []string{"CAP-RGA-055"},
		Timeout: 3 * time.Minute,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, perr, err := s.Observe(ctx, M{"scope": "element", "window": tm.title, "target": "textbox", "text": true, "tail": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			if o.Element == nil || o.Element.Value == nil || !strings.Contains(*o.Element.Value, "TAIL-MARK") {
				return parity.Outcome{Class: "no_tail", Detail: "the tail does not hold the last line"}, nil
			}
			if !o.Element.Truncated {
				return parity.Outcome{Class: "not_truncated", Detail: "the cut text is not flagged"}, nil
			}
			return parity.Outcome{OK: true}, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "snapshot", M{"window": tm.title})
			if err != nil {
				return parity.Outcome{}, err
			}
			id := firstID(d.JSON, "textbox")
			if id == "" {
				return parity.Outcome{Class: "target_not_found", Detail: clip(d.Text, 300)}, nil
			}
			rd, err := s.Desk(ctx, "read", M{"id": id, "text": true, "tail": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			switch {
			case !strings.Contains(rd.Text, "TAIL-MARK"):
				return parity.Outcome{Class: "no_tail", Detail: clip(rd.Text, 200)}, nil
			case !strings.Contains(rd.Text, `"truncated":true`):
				return parity.Outcome{Class: "not_truncated"}, nil
			}
			return parity.Outcome{OK: true}, nil
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	}))
}

// firstID finds the id of the first node of a role in a shuttle snapshot.
func firstID(snap M, role string) string {
	nodes, _ := snap["nodes"].([]any)
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if ok && m["role"] == role {
			if id, ok := m["id"].(string); ok {
				return id
			}
		}
	}
	return ""
}

var _ = view.RawWindow{}
