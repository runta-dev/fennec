package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"fennec/cua/internal/browser"
	"fennec/cua/internal/chromium"
	"fennec/cua/internal/daemon"
)

// browserOps are the daemon's control operations for the bundled
// Chromium, for the CLI and Fennec's UI: the daemon owns the browser, so
// an import writes into the running instance.
func browserOps(m *browser.Manager, profile string) map[string]func(context.Context, json.RawMessage) (any, error) {
	return map[string]func(context.Context, json.RawMessage) (any, error){
		"browser.profiles": func(context.Context, json.RawMessage) (any, error) {
			return m.ChromeProfiles()
		},
		"browser.import": func(ctx context.Context, args json.RawMessage) (any, error) {
			var o browser.ImportOptions
			if len(args) > 0 {
				if err := json.Unmarshal(args, &o); err != nil {
					return nil, fmt.Errorf("bad import options: %w", err)
				}
			}
			return m.ImportFromChrome(ctx, o)
		},
		"browser.status": func(context.Context, json.RawMessage) (any, error) {
			last, err := m.LastImport()
			if err != nil {
				return nil, err
			}
			// How the managed profile's cookies are encrypted, and what a
			// move from the mock keychain carried over (counts only).
			cookies, err := chromium.ReadCookieState(profile)
			if err != nil {
				return nil, err
			}
			return map[string]any{"available": m.Available(), "lastImport": last, "cookieStore": cookies}, nil
		},
	}
}

// browserCmd is `fennec-cua browser profiles | import | status`: it asks
// the running daemon and prints its JSON answer.
func browserCmd(ctx context.Context, paths daemon.Paths, args []string) error {
	return browserCmdTo(ctx, paths, args, os.Stdout)
}

func browserCmdTo(ctx context.Context, paths daemon.Paths, args []string, out io.Writer) error {
	usage := errors.New("usage: fennec-cua browser profiles | import [--profile NAME] [--replace] | status")
	if len(args) == 0 {
		return usage
	}
	var op string
	var payload json.RawMessage
	timeout := 10 * time.Second
	switch args[0] {
	case "profiles", "status":
		if len(args) != 1 {
			return usage
		}
		op = "browser." + args[0]
	case "import":
		fs := flag.NewFlagSet("import", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var o browser.ImportOptions
		fs.StringVar(&o.Profile, "profile", "", "Chrome profile directory or name (default: the last used)")
		fs.BoolVar(&o.Replace, "replace", false, "clear the bundled Chromium's cookies first")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return usage
		}
		payload, _ = json.Marshal(o)
		op = "browser.import"
		// The Keychain prompt waits for the user.
		timeout = 3 * time.Minute
	default:
		return usage
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rep, err := daemon.ControlArgs(cctx, paths, op, payload)
	if err != nil {
		if rep.Error != "" {
			return errors.New(rep.Error)
		}
		return fmt.Errorf("browser %s: %w (is the daemon running?)", args[0], err)
	}
	data, _ := json.MarshalIndent(rep.Status, "", "  ")
	_, err = fmt.Fprintln(out, string(data))
	return err
}
