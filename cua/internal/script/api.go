package script

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"fennec/cua/internal/protocol"
)

// install puts the globals every runtime sees: console, setTimeout and
// sleep.
func (r *runner) install() {
	vm := r.vm
	console := vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		prefix := ""
		if name == "warn" || name == "error" {
			prefix = name + ": "
		}
		_ = console.Set(name, func(call goja.FunctionCall) goja.Value {
			parts := make([]string, len(call.Arguments))
			for i, a := range call.Arguments {
				parts[i] = show(a)
			}
			r.logs.write(prefix + strings.Join(parts, " ") + "\n")
			return goja.Undefined()
		})
	}
	_ = vm.Set("console", console)
	_ = vm.Set("setTimeout", func(fn goja.Callable, ms int64) int {
		ctx, loop := r.ctx, r.loop
		go func() {
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
				post(ctx, loop, func() { _, _ = fn(goja.Undefined()) })
			case <-ctx.Done():
			}
		}()
		return 0
	})
	_ = vm.Set("sleep", func(ms int64) *goja.Promise {
		p, resolve, _ := vm.NewPromise()
		ctx, loop := r.ctx, r.loop
		go func() {
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
				post(ctx, loop, func() { _ = resolve(goja.Undefined()) })
			case <-ctx.Done():
			}
		}()
		return p
	})
	vm.SetPromiseRejectionTracker(func(p *goja.Promise, op goja.PromiseRejectionOperation) {
		if op == goja.PromiseRejectionReject {
			r.unhandled[p] = struct{}{}
		} else {
			delete(r.unhandled, p)
		}
	})
}

// installRunAPI puts the run tool's cua object.
func (r *runner) installRunAPI() {
	vm := r.vm
	cua := vm.NewObject()
	_ = cua.Set("input", r.h.Input())
	for _, tool := range []string{protocol.ToolObserve, protocol.ToolAct, protocol.ToolWait, protocol.ToolFlow, protocol.ToolStatus} {
		_ = cua.Set(tool, func(args goja.Value) *goja.Promise { return r.tool(tool, export(args)) })
	}
	// Shorthands for single input steps; a target (window, app or pid)
	// passes through so input reaches that app, not the front window.
	_ = cua.Set("key", func(keys string, target goja.Value) *goja.Promise {
		return r.tool(protocol.ToolAct, step(map[string]any{"key": keys}, target))
	})
	_ = cua.Set("type", func(text string, target goja.Value) *goja.Promise {
		return r.tool(protocol.ToolAct, step(map[string]any{"type": text}, target))
	})
	_ = cua.Set("click", func(x, y float64, opts goja.Value) *goja.Promise {
		s := map[string]any{"click": map[string]any{"point": map[string]any{"x": x, "y": y}}}
		return r.tool(protocol.ToolAct, step(s, opts))
	})
	_ = cua.Set("press", func(target string) *goja.Promise {
		return r.tool(protocol.ToolAct, map[string]any{"steps": []any{map[string]any{"press": target}}})
	})
	model := vm.NewObject()
	_ = model.Set("ask", func(prompt string, images goja.Value) *goja.Promise {
		return r.model("ask", map[string]any{"prompt": prompt, "images": pngs(images)})
	})
	_ = model.Set("decide", func(args goja.Value) *goja.Promise { return r.model("decide", export(args)) })
	_ = model.Set("ground", func(args goja.Value) *goja.Promise { return r.model("ground", export(args)) })
	_ = cua.Set("model", model)
	_ = vm.Set("cua", cua)
}

// step builds a one-step act request, merging a target's window, app, pid
// and step options into the step.
func step(s map[string]any, target goja.Value) map[string]any {
	if t, ok := export(target).(map[string]any); ok {
		for k, v := range t {
			s[k] = v
		}
	}
	return map[string]any{"steps": []any{s}}
}

// pngs accepts no image, one buffer or a list of them, as base64 strings.
func pngs(v goja.Value) []string {
	var out []string
	add := func(x any) {
		switch b := x.(type) {
		case goja.ArrayBuffer:
			out = append(out, base64.StdEncoding.EncodeToString(b.Bytes()))
		case []byte:
			out = append(out, base64.StdEncoding.EncodeToString(b))
		case string:
			out = append(out, b)
		}
	}
	switch x := export(v).(type) {
	case nil:
	case []any:
		for _, item := range x {
			add(item)
		}
	default:
		add(x)
	}
	return out
}

// post schedules work on an evaluation's loop unless that evaluation has
// ended. Goroutines capture ctx and loop when they start, so work left
// behind by an ended evaluation of a persistent runtime never runs in a
// later one.
func post(ctx context.Context, loop chan func(), f func()) {
	select {
	case loop <- f:
	case <-ctx.Done():
	}
}

// tool calls a host tool off the loop and settles the promise on it.
func (r *runner) tool(name string, args any) *goja.Promise {
	return r.async(func(ctx context.Context, h Host) (any, *protocol.Error) {
		data, err := json.Marshal(args)
		if err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}
		}
		return h.Tool(ctx, name, data)
	})
}

func (r *runner) model(op string, args any) *goja.Promise {
	return r.async(func(ctx context.Context, h Host) (any, *protocol.Error) {
		data, err := json.Marshal(args)
		if err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}
		}
		return h.Model(ctx, op, data)
	})
}

func (r *runner) async(f func(ctx context.Context, h Host) (any, *protocol.Error)) *goja.Promise {
	p, resolve, reject := r.vm.NewPromise()
	ctx, loop, h := r.ctx, r.loop, r.h
	go func() {
		v, perr := f(ctx, h)
		// Round-trip through JSON so the script sees plain objects.
		var plain any
		if v != nil {
			data, _ := json.Marshal(v)
			_ = json.Unmarshal(data, &plain)
		}
		post(ctx, loop, func() {
			if perr != nil {
				e := r.vm.NewGoError(fmt.Errorf("%s: %s", perr.Code, perr.Message))
				_ = e.Set("code", string(perr.Code))
				if perr.Hint != "" {
					_ = e.Set("hint", perr.Hint)
				}
				if plain != nil {
					// A failure with a report (a run) keeps it.
					_ = e.Set("report", r.vm.ToValue(plain))
				}
				_ = reject(e)
				return
			}
			_ = resolve(r.vm.ToValue(plain))
		})
	}()
	return p
}

func show(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	switch x := v.Export().(type) {
	case string:
		return x
	case map[string]any, []any:
		data, err := json.Marshal(x)
		if err == nil {
			return string(data)
		}
	}
	return v.String()
}
