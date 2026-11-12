//go:build scenario

package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/parity"
)

// The harness's posted input stands in for the user a recording watches.
// Virtual key codes of a US layout.
var keyCodes = map[rune]int{'a': 0, 's': 1, 'd': 2, 'f': 3, 'h': 4, 'g': 5, 'z': 6, 'x': 7, 'c': 8, 'v': 9, 'b': 11, 'q': 12, 'w': 13, 'e': 14, 'r': 15, 'y': 16, 't': 17, 'o': 31, 'u': 32, 'i': 34, 'p': 35, 'l': 37, 'j': 38, 'k': 40, 'n': 45, 'm': 46}

func userType(s string) {
	for _, r := range s {
		if c, ok := keyCodes[r]; ok {
			userKey(c)
			time.Sleep(40 * time.Millisecond)
		}
	}
}

// demo clicks into TextEdit's document and types a word, as a user would.
func demo(ctx context.Context) error {
	w, err := window(ctx, "TextEdit", "rec.txt")
	if err != nil {
		return err
	}
	x, y := center(*w.Bounds)
	userClick(x, y+40)
	if err := sleep(ctx, 400); err != nil {
		return err
	}
	userType("hello")
	return sleep(ctx, 1200)
}

// CAP-RGB-073: a recorded demonstration in TextEdit becomes a draft with
// a launch step and the typed run.
func TestRecordTextEditDraft(t *testing.T) {
	draftOK := func(text string) parity.Outcome {
		l := strings.ToLower(text)
		switch {
		case !strings.Contains(l, "textedit"):
			return parity.Outcome{Class: "no_app_step", Detail: clip(text, 400)}
		case !strings.Contains(text, "hello"):
			return parity.Outcome{Class: "no_typed_run", Detail: clip(text, 400)}
		}
		return parity.Outcome{OK: true}
	}
	run(t, Case{
		Caps:    []string{"CAP-RGB-073"},
		Setup:   func(ctx context.Context) error { _, err := textEdit(ctx, "rec.txt", ""); return err },
		Cleanup: func(ctx context.Context) { quitApp(ctx, "TextEdit") },
		Timeout: 3 * time.Minute,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			r, err := s.Tool(ctx, "flow", M{"op": "record_start", "apps": []string{"TextEdit"}})
			if err != nil || r.IsError {
				return parity.Outcome{Class: "record_start", Detail: r.Text()}, err
			}
			if err := demo(ctx); err != nil {
				return parity.Outcome{}, err
			}
			r, err = s.Tool(ctx, "flow", M{"op": "record_stop"})
			if err != nil || r.IsError {
				return parity.Outcome{Class: "record_stop", Detail: r.Text()}, err
			}
			return draftOK(stepsOf(r.Text(), "flow")), nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			r, err := s.Tool(ctx, "record_start", M{"apps": []string{"TextEdit"}})
			if err != nil || r.IsError {
				return parity.Outcome{Class: "record_start", Detail: r.Text()}, err
			}
			if err := demo(ctx); err != nil {
				return parity.Outcome{}, err
			}
			r, err = s.Tool(ctx, "record_stop", M{})
			if err != nil || r.IsError {
				return parity.Outcome{Class: "record_stop", Detail: r.Text()}, err
			}
			return draftOK(stepsOf(r.Text(), "")), nil
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// stepsOf renders a draft's steps as JSON text; under, when set, is the key
// that holds the draft.
func stepsOf(text, under string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &m) != nil {
		return text
	}
	if under != "" {
		var inner map[string]json.RawMessage
		if json.Unmarshal(m[under], &inner) == nil {
			m = inner
		}
	}
	return fmt.Sprint(string(m["steps"]))
}

// CAP-RGB-082: a click recorded in Safari carries the page address and the
// element's DOM id.
func TestRecordSafariPage(t *testing.T) {
	clickActivate := func(ctx context.Context) error {
		w, err := window(ctx, "Safari", "")
		if err != nil {
			return err
		}
		n, err := element(ctx, w, "button", "Activate")
		if err != nil || n.Bounds == nil {
			return fmt.Errorf("no Activate button: %v", err)
		}
		x, y := center(*n.Bounds)
		userClick(x, y)
		return sleep(ctx, 1200)
	}
	draftOK := func(text string) parity.Outcome {
		switch {
		case !strings.Contains(text, "page.html"):
			return parity.Outcome{Class: "no_page", Detail: clip(text, 400)}
		case !strings.Contains(text, `"act"`):
			return parity.Outcome{Class: "no_dom_id", Detail: clip(text, 400)}
		}
		return parity.Outcome{OK: true}
	}
	record := func(start, stop string, startArgs M, under string) Driver {
		return func(ctx context.Context, s *Session) (parity.Outcome, error) {
			r, err := s.Tool(ctx, start, startArgs)
			if err != nil || r.IsError {
				return parity.Outcome{Class: "record_start", Detail: r.Text()}, err
			}
			if err := clickActivate(ctx); err != nil {
				return parity.Outcome{}, err
			}
			args := M{}
			if start == "flow" {
				args = M{"op": "record_stop"}
			}
			r, err = s.Tool(ctx, stop, args)
			if err != nil || r.IsError {
				return parity.Outcome{Class: "record_stop", Detail: r.Text()}, err
			}
			return draftOK(stepsOf(r.Text(), under)), nil
		}
	}
	run(t, safariCase(Case{
		Caps:    []string{"CAP-RGB-082"},
		Setup:   openPage,
		Fennec:  record("flow", "flow", M{"op": "record_start", "apps": []string{"Safari"}}, "flow"),
		Shuttle: record("record_start", "record_stop", M{"apps": []string{"Safari"}}, ""),
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	}))
}
