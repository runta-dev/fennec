package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/mcpserver"
)

// DefaultSession is the named session `fennec-cua js` uses when neither
// --session nor SessionEnv names one.
const DefaultSession = "default"

// SessionEnv names the session for `fennec-cua js` when --session is not
// given, so an agent can set it once for a whole task.
const SessionEnv = "FENNEC_CUA_SESSION"

// JSClientName is the MCP client name the command line presents.
const JSClientName = "fennec-cua-cli"

// MaxStdinCode bounds the code read from standard input.
const MaxStdinCode = 4 << 20

// JSOptions are `fennec-cua js` options.
type JSOptions struct {
	// Session is the named session whose REPL runtime runs the code.
	Session string
	// Title is the js call's progress label.
	Title string
	// Out is the directory for images (the system temporary directory
	// when empty).
	Out string
	// Code is the JavaScript; empty means read standard input.
	Code string
	// Stdin is set when the code comes from standard input.
	Stdin bool
	// Timeout bounds the whole command, daemon start included.
	Timeout time.Duration
}

// JSUsage describes the js commands.
const JSUsage = `usage: fennec-cua js [--session NAME] [--title TEXT] [--out DIR] [--timeout DURATION] [CODE | -]
       fennec-cua js-reset [--session NAME]
       fennec-cua js-end [--session NAME]
CODE is JavaScript for the persistent REPL (the MCP tool "js"); without CODE, or with "-",
it is read from standard input. Calls with the same session name share one runtime, so
bindings (var app = await cua.getApp("TextEdit")) survive between invocations. The session
is $` + SessionEnv + ` or "` + DefaultSession + `" unless --session names one; it ends after two idle
hours or with js-end. Images are saved to files (--out DIR) and their paths printed.`

// ParseJSArgs reads `fennec-cua js` arguments (after "js"). env looks up
// environment variables (os.Getenv when nil).
func ParseJSArgs(args []string, env func(string) string) (JSOptions, error) {
	if env == nil {
		env = os.Getenv
	}
	o := JSOptions{Session: env(SessionEnv), Timeout: 5 * time.Minute}
	fs := flag.NewFlagSet("js", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Session, "session", o.Session, "")
	fs.StringVar(&o.Title, "title", "", "")
	fs.StringVar(&o.Out, "out", "", "")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "")
	if err := fs.Parse(args); err != nil {
		return JSOptions{}, fmt.Errorf("%v\n%s", err, JSUsage)
	}
	if o.Session == "" {
		o.Session = DefaultSession
	}
	if !mcpserver.ValidSessionName(o.Session) {
		return JSOptions{}, fmt.Errorf("session name %q is not 1 to 64 letters, digits, '.', '_' or '-'", o.Session)
	}
	if o.Timeout <= 0 {
		return JSOptions{}, errors.New("--timeout must be positive")
	}
	rest := fs.Args()
	switch {
	case len(rest) == 0 || len(rest) == 1 && rest[0] == "-":
		o.Stdin = true
	case len(rest) == 1:
		o.Code = rest[0]
	default:
		return JSOptions{}, fmt.Errorf("pass the code as one quoted argument or on standard input\n%s", JSUsage)
	}
	if o.Title == "" {
		o.Title = "Running a command-line call"
	}
	return o, nil
}

// ParseSessionArgs reads `fennec-cua js-reset` and `js-end` arguments.
func ParseSessionArgs(args []string, env func(string) string) (string, error) {
	if env == nil {
		env = os.Getenv
	}
	session := env(SessionEnv)
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&session, "session", session, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return "", errors.New(JSUsage)
	}
	if session == "" {
		session = DefaultSession
	}
	if !mcpserver.ValidSessionName(session) {
		return "", fmt.Errorf("session name %q is not 1 to 64 letters, digits, '.', '_' or '-'", session)
	}
	return session, nil
}

// ReadCode reads the code from r, refusing more than MaxStdinCode bytes and
// empty code.
func ReadCode(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxStdinCode+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxStdinCode {
		return "", fmt.Errorf("the code on standard input exceeds %d MiB", MaxStdinCode>>20)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("no code: pass it as an argument or on standard input")
	}
	return string(data), nil
}

// EnsureDaemon returns once the daemon answers at socket, starting it with
// start when nothing listens there. wait bounds the wait after a start.
func EnsureDaemon(ctx context.Context, socket string, start func() (<-chan error, error), wait time.Duration) error {
	if answers(ctx, socket) {
		return nil
	}
	exited, err := start()
	if err != nil {
		return fmt.Errorf("start the fennec-cua daemon: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		if answers(ctx, socket) {
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("the fennec-cua daemon exited before answering (%v); run `fennec-cua serve` to see why", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the fennec-cua daemon did not answer at %s within %s; see `fennec-cua doctor`", socket, wait)
		}
	}
}

func answers(ctx context.Context, socket string) bool {
	c, err := dialUnix(ctx, socket)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// JS runs code in the named session's runtime and returns the result.
func JS(ctx context.Context, socket string, o JSOptions) (mcpclient.ToolResult, error) {
	c, err := mcpclient.DialSession(ctx, socket, JSClientName, o.Session)
	if err != nil {
		return mcpclient.ToolResult{}, err
	}
	defer c.Close()
	return c.Tool(ctx, "js", map[string]any{"code": o.Code, "title": o.Title})
}

// JSReset discards the named session's runtime (its bindings); its browser
// tabs stay.
func JSReset(ctx context.Context, socket, session string) (mcpclient.ToolResult, error) {
	c, err := mcpclient.DialSession(ctx, socket, JSClientName, session)
	if err != nil {
		return mcpclient.ToolResult{}, err
	}
	defer c.Close()
	return c.Tool(ctx, "js_reset", map[string]any{})
}

// JSEnd ends the named session: its runtime goes and its unmarked agent
// tabs close.
func JSEnd(ctx context.Context, socket, session string) error {
	c, err := mcpclient.DialSession(ctx, socket, JSClientName, session)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.EndSession(ctx)
}

// PrintResult writes a tool result: text parts as they are, images saved to
// files in dir with their paths. It reports whether the result is an error.
func PrintResult(w io.Writer, res mcpclient.ToolResult, dir string) (bool, error) {
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			if _, err := fmt.Fprintln(w, c.Text); err != nil {
				return res.IsError, err
			}
		case "image":
			p, err := SaveCapture(dir, c.Data)
			if err != nil {
				return res.IsError, err
			}
			if _, err := fmt.Fprintln(w, "[image saved: "+p+"]"); err != nil {
				return res.IsError, err
			}
		}
	}
	return res.IsError, nil
}
