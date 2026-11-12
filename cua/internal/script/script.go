// Package script runs the run tool's TypeScript or JavaScript modules in
// process: esbuild bundles and transpiles, goja executes. A script sees only
// the cua API the host hands it: no file system, no processes, no
// environment, no model keys.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"

	"fennec/cua/internal/protocol"
)

// Limits.
const (
	DefaultTimeout = 90 * time.Second
	MaxTimeout     = 110 * time.Second
	LogCap         = 32 << 10
)

// Host is what a script's cua API reaches.
type Host interface {
	// Tool runs one of fennec-cua's tools (observe, act, wait, flow,
	// status) as the run's own caller.
	Tool(ctx context.Context, name string, args json.RawMessage) (any, *protocol.Error)
	// Model runs a model call (ask, decide, ground) and returns its
	// result; usage is tallied by the host.
	Model(ctx context.Context, op string, args json.RawMessage) (any, *protocol.Error)
	// Usage is the run's model usage so far.
	Usage() protocol.Usage
	// Input is the configured input mode.
	Input() string
}

// Load returns the module source for a request: inline code, or the file at
// an absolute path.
func Load(req protocol.RunRequest) (code, dir string, perr *protocol.Error) {
	switch {
	case req.Code != "" && req.Path != "":
		return "", "", &protocol.Error{Code: protocol.ErrInvalid, Message: "pass code or path, not both"}
	case req.Code != "":
		return req.Code, "", nil
	case req.Path == "":
		return "", "", &protocol.Error{Code: protocol.ErrInvalid, Message: "pass code or an absolute path"}
	case !filepath.IsAbs(req.Path):
		return "", "", &protocol.Error{Code: protocol.ErrInvalid, Message: "path must be absolute: " + req.Path, Hint: "pass the script's absolute path, or its source as code"}
	}
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return "", "", &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}
	}
	return string(data), filepath.Dir(req.Path), nil
}

// bundle turns a module and its relative imports into one CommonJS script
// goja can run. Imports of node:* or packages fail: scripts get the cua API
// only.
func bundle(code, dir string) (string, error) {
	res := api.Build(api.BuildOptions{
		Stdin:    &api.StdinOptions{Contents: code, ResolveDir: dir, Sourcefile: "script.ts", Loader: api.LoaderTS},
		Bundle:   true,
		Write:    false,
		Format:   api.FormatCommonJS,
		Platform: api.PlatformNeutral,
		Target:   api.ES2022,
		// goja has no async generators; esbuild lowers them.
		Supported: map[string]bool{"async-generator": false, "for-await": false},
		LogLevel:  api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		var msgs []string
		for _, m := range res.Errors {
			loc := ""
			if m.Location != nil {
				loc = fmt.Sprintf("%s:%d:%d: ", m.Location.File, m.Location.Line, m.Location.Column)
			}
			msgs = append(msgs, loc+m.Text)
		}
		return "", errors.New(strings.Join(msgs, "\n"))
	}
	return string(res.OutputFiles[0].Contents), nil
}

// logs keeps the first LogCap bytes of console output.
type logs struct {
	mu  sync.Mutex
	b   strings.Builder
	cut bool
}

func (l *logs) write(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() >= LogCap {
		l.cut = true
		return
	}
	if room := LogCap - l.b.Len(); len(s) > room {
		s, l.cut = s[:room], true
	}
	l.b.WriteString(s)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cut {
		return l.b.String() + "\n[logs cut at 32 KiB]"
	}
	return l.b.String()
}

// Run executes the request's module and reports. The report is complete
// even after a timeout or a stop: logs and usage so far are kept.
func Run(ctx context.Context, h Host, req protocol.RunRequest) protocol.RunResult {
	start := time.Now()
	timeout := DefaultTimeout
	if req.TimeoutS > 0 {
		timeout = min(time.Duration(req.TimeoutS)*time.Second, MaxTimeout)
	}
	r := &runner{h: h, logs: &logs{}, loop: make(chan func(), 64), unhandled: map[*goja.Promise]struct{}{}}
	report := func(result any, kind, msg string) protocol.RunResult {
		out := protocol.RunResult{Result: result, Logs: r.logs.String(), DurationMs: int(time.Since(start).Milliseconds())}
		if u := h.Usage(); len(u) > 0 {
			out.Usage = u
		}
		if kind != "" {
			out.Error = &protocol.RunError{Kind: kind, Message: msg}
		}
		return out
	}
	code, dir, perr := Load(req)
	if perr != nil {
		return report(nil, protocol.RunImport, perr.Message)
	}
	js, err := bundle(code, dir)
	if err != nil {
		return report(nil, protocol.RunImport, err.Error())
	}
	rctx, cancel := context.WithTimeoutCause(ctx, timeout, errTimeout)
	defer cancel()
	r.ctx = rctx
	vm := goja.New()
	r.vm = vm
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	r.install()
	stop := context.AfterFunc(rctx, func() { vm.Interrupt(errInterrupted) })
	defer stop()

	module := vm.NewObject()
	exports := vm.NewObject()
	_ = module.Set("exports", exports)
	_ = vm.Set("module", module)
	_ = vm.Set("exports", exports)
	if _, err := vm.RunScript("script.js", js); err != nil {
		if kind, msg, ok := r.ended(); ok {
			return report(nil, kind, msg)
		}
		return report(nil, protocol.RunImport, errText(err))
	}
	mainFn, ok := goja.AssertFunction(module.Get("exports").ToObject(vm).Get("main"))
	if !ok {
		return report(nil, protocol.RunNoMain, "the module exports no main function")
	}
	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	v, err := mainFn(goja.Undefined(), vm.ToValue(params))
	if err != nil {
		if kind, msg, ok := r.ended(); ok {
			return report(nil, kind, msg)
		}
		return report(nil, protocol.RunException, errText(err))
	}
	result, kind, msg := r.settle(v)
	return report(result, kind, msg)
}

var (
	errTimeout     = errors.New("the run timed out")
	errInterrupted = errors.New("interrupted")
)

type runner struct {
	ctx       context.Context
	vm        *goja.Runtime
	h         Host
	logs      *logs
	loop      chan func()
	pending   sync.WaitGroup
	unhandled map[*goja.Promise]struct{}
}

// ended reports a timeout or a stop that ended the run.
func (r *runner) ended() (kind, msg string, ok bool) {
	if r.ctx.Err() == nil {
		return "", "", false
	}
	if errors.Is(context.Cause(r.ctx), errTimeout) {
		return protocol.RunTimeout, "the run timed out", true
	}
	return protocol.RunStopped, "the run was stopped", true
}

// settle runs the event loop until main's value (or promise) settles, then
// briefly drains work main left behind, reporting an error that escapes
// after main returned as a crash.
func (r *runner) settle(v goja.Value) (any, string, string) {
	p, isPromise := v.Export().(*goja.Promise)
	for isPromise && p.State() == goja.PromiseStatePending {
		select {
		case f := <-r.loop:
			if !r.call(f) {
				kind, msg, _ := r.ended()
				return nil, kind, msg
			}
		case <-r.ctx.Done():
			kind, msg, _ := r.ended()
			return nil, kind, msg
		}
	}
	var result goja.Value = v
	if isPromise {
		if p.State() == goja.PromiseStateRejected {
			return nil, protocol.RunException, errText(p.Result())
		}
		result = p.Result()
	}
	out := export(result)
	// Let callbacks main started but did not await finish, so their errors
	// are seen.
	drain := time.After(50 * time.Millisecond)
	for {
		select {
		case f := <-r.loop:
			r.call(f)
			continue
		case <-drain:
		case <-r.ctx.Done():
		}
		break
	}
	for rej := range r.unhandled {
		if !isPromise || rej != p {
			return out, protocol.RunCrash, errText(rej.Result())
		}
	}
	return out, "", ""
}

// call runs a loop task, reporting false when the run was interrupted.
func (r *runner) call(f func()) (ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			if _, interrupted := rec.(*goja.InterruptedError); interrupted || r.ctx.Err() != nil {
				ok = false
				return
			}
			panic(rec)
		}
	}()
	f()
	return r.ctx.Err() == nil
}

func export(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

func errText(v any) string {
	switch e := v.(type) {
	case *goja.Exception:
		if s := e.Value(); s != nil {
			if obj, ok := s.Export().(map[string]any); ok {
				if stack, ok := obj["stack"].(string); ok && stack != "" {
					return stack
				}
			}
			if o := s.ToObject(nil); o != nil {
				if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) {
					return st.String()
				}
			}
			return s.String()
		}
		return e.Error()
	case goja.Value:
		if o, ok := v.(*goja.Object); ok {
			if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) {
				return st.String()
			}
		}
		return e.String()
	case error:
		return e.Error()
	}
	return fmt.Sprint(v)
}
