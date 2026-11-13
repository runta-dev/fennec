package script

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"fennec/cua/internal/protocol"
)

// REPL limits.
const (
	// ReplTimeout bounds one js call unless the engine sets another.
	ReplTimeout = 90 * time.Second
	// OutputCap bounds a call's text output (about 25k tokens).
	OutputCap = 100_000
	// MaxImages bounds the images one call emits.
	MaxImages = 16
)

//go:embed prelude.js
var preludeJS string

//go:embed replguide.md
var replGuide string

// Guide is the API documentation the first call of a session shows.
func Guide() string { return replGuide }

// Part is one piece of a call's output: text or an image.
type Part struct {
	Text  string
	Image *Picture
}

// Picture is an emitted image.
type Picture struct {
	MIME string
	Data string // base64
}

// EvalResult is one js call's outcome.
type EvalResult struct {
	Parts []Part
	// Err is the error text ("Error: ...") when the call failed; the parts
	// hold what was written before it.
	Err string
	// Fresh is set when the call created the runtime (the session's first
	// call, or the first after a reset).
	Fresh bool
	Wall  time.Duration
}

// REPL is one session's persistent JavaScript runtime. Calls run one at a
// time; top-level bindings persist between them until Close.
type REPL struct {
	// Timeout bounds each call; zero means ReplTimeout.
	Timeout time.Duration
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time

	sem chan struct{}

	mu     sync.Mutex
	cancel context.CancelCauseFunc
	closed bool

	r   *runner
	out *output
	ax  map[int]*axMemory
}

// NewREPL makes an empty session runtime; the first Eval builds it.
func NewREPL() *REPL {
	return &REPL{sem: make(chan struct{}, 1), ax: map[int]*axMemory{}}
}

var errReset = errors.New("the runtime was reset while this call ran")

// Close discards the runtime and ends a call in flight.
func (p *REPL) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.cancel != nil {
		p.cancel(errReset)
	}
}

func (p *REPL) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Eval runs one cell with h as its host.
func (p *REPL) Eval(ctx context.Context, h Host, code string) EvalResult {
	start := p.now()
	res := EvalResult{}
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		res.Err = "Error: the call was cancelled before it started"
		res.Wall = p.now().Sub(start)
		return res
	}
	defer func() { <-p.sem }()
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = ReplTimeout
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		res.Err = "Error: " + errReset.Error()
		return res
	}
	cctx, cancel := context.WithCancelCause(ctx)
	ectx, tcancel := context.WithTimeoutCause(cctx, timeout, errTimeout)
	p.cancel = cancel
	p.mu.Unlock()
	defer func() {
		tcancel()
		cancel(nil)
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
	}()

	if p.r == nil {
		p.r = &runner{}
		res.Fresh = true
	}
	r := p.r
	out := &output{}
	p.out = out
	r.ctx, r.loop, r.h, r.unhandled = ectx, make(chan func(), 64), h, map[*goja.Promise]struct{}{}
	r.logs = &logs{max: OutputCap}
	if res.Fresh {
		p.build()
	}
	vm := r.vm
	fired := make(chan struct{})
	stop := context.AfterFunc(ectx, func() { vm.Interrupt(errInterrupted); close(fired) })
	defer func() {
		if !stop() {
			<-fired
		}
		vm.ClearInterrupt()
	}()

	finish := func(errText string) EvalResult {
		res.Parts = out.finish()
		res.Err = errText
		res.Wall = p.now().Sub(start)
		return res
	}
	prep, err := prepareCell(code)
	if err != nil {
		return finish(err.Error())
	}
	g := vm.GlobalObject()
	for _, name := range prep.Globals {
		if g.Get(name) == nil {
			_ = g.Set(name, goja.Undefined())
		}
	}
	v, err := vm.RunScript("cell.js", prep.Source)
	if err != nil {
		if msg, ok := p.ended(ectx, timeout); ok {
			return finish(msg)
		}
		return finish(errorLine(err))
	}
	pr, ok := v.Export().(*goja.Promise)
	if !ok {
		return finish("")
	}
	for pr.State() == goja.PromiseStatePending {
		select {
		case f := <-r.loop:
			if !r.call(f) {
				msg, _ := p.ended(ectx, timeout)
				return finish(msg)
			}
		case <-ectx.Done():
			msg, _ := p.ended(ectx, timeout)
			return finish(msg)
		}
	}
	if pr.State() == goja.PromiseStateRejected {
		return finish(errorLine(pr.Result()))
	}
	// The last expression's value is not echoed, as in the reference: the
	// self-displaying calls already show theirs, and { emit: false } must
	// stay quiet. Output comes from nodeRepl.write and console.
	return finish("")
}

// ended names a timeout, a stop or a reset that ended a call.
func (p *REPL) ended(ctx context.Context, timeout time.Duration) (string, bool) {
	if ctx.Err() == nil {
		return "", false
	}
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errTimeout):
		return fmt.Sprintf("Error: the call ran past its %d-second limit and was ended; the runtime and its bindings are kept. Split long work across calls.", int(timeout.Seconds())), true
	case errors.Is(cause, errReset):
		return "Error: " + errReset.Error(), true
	}
	return "Error: the call was stopped: the user stopped fennec-cua, or the client cancelled it. Input may have been sent before the stop; read the state again before resending.", true
}

// errorLine renders an uncaught error as the reference does: an Error's
// message alone, without its name or stack ("tab is not defined"); a thrown
// value of another kind as its text.
func errorLine(v any) string {
	if exc, ok := v.(*goja.Exception); ok {
		if val := exc.Value(); val != nil {
			v = val
		}
	}
	if o, ok := v.(*goja.Object); ok {
		if msg := o.Get("message"); msg != nil && !goja.IsUndefined(msg) {
			return msg.String()
		}
		return o.String()
	}
	if val, ok := v.(goja.Value); ok && val != nil {
		return val.String()
	}
	return errText(v)
}

// output collects a call's parts within the caps.
type output struct {
	mu      sync.Mutex
	parts   []Part
	used    int
	dropped int
	images  int
	extra   int // images refused over MaxImages
}

// text adds one text part.
func (o *output) text(s string) {
	o.add(s, false)
}

// line appends to the last text part on a new line (console output).
func (o *output) line(s string) {
	o.add(s, true)
}

func (o *output) add(s string, join bool) {
	if s == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	room := OutputCap - o.used
	if room <= 0 {
		o.dropped += len(s)
		return
	}
	if len(s) > room {
		kept := cutUTF8(s, room)
		o.dropped += len(s) - len(kept)
		s = kept
	}
	o.used += len(s)
	if n := len(o.parts); join && n > 0 && o.parts[n-1].Image == nil {
		o.parts[n-1].Text += s
		return
	}
	o.parts = append(o.parts, Part{Text: s})
}

func (o *output) image(mime string, data []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.images >= MaxImages {
		o.extra++
		return false
	}
	o.images++
	o.parts = append(o.parts, Part{Image: &Picture{MIME: mime, Data: base64.StdEncoding.EncodeToString(data)}})
	return true
}

func (o *output) finish() []Part {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := append([]Part(nil), o.parts...)
	if o.dropped > 0 {
		out = append(out, Part{Text: fmt.Sprintf("[output cut at %d characters: %d more were dropped; write less, or slice what you write]", OutputCap, o.dropped)})
	}
	if o.extra > 0 {
		out = append(out, Part{Text: fmt.Sprintf("[%d more images were dropped: a call shows at most %d]", o.extra, MaxImages)})
	}
	return out
}

// build makes the runtime: timers, console, nodeRepl, and the cua and
// fennec objects from the prelude.
func (p *REPL) build() {
	r := p.r
	vm := goja.New()
	r.vm = vm
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	r.install()
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
			p.out.line(prefix + strings.Join(parts, " ") + "\n")
			return goja.Undefined()
		})
	}
	_ = vm.Set("console", console)
	host := vm.NewObject()
	_ = host.Set("call", func(name string, args goja.Value) *goja.Promise { return r.tool(name, export(args)) })
	_ = host.Set("model", func(op string, args goja.Value) *goja.Promise { return r.model(op, export(args)) })
	_ = host.Set("write", func(v goja.Value) { p.out.text(p.render(v)) })
	_ = host.Set("emitImage", func(v goja.Value) *goja.Promise {
		pr, resolve, reject := vm.NewPromise()
		if err := p.emit(v); err != nil {
			_ = reject(vm.NewGoError(err))
		} else {
			_ = resolve(goja.Undefined())
		}
		return pr
	})
	_ = host.Set("bytes", func(b64 string) goja.Value {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			panic(vm.NewTypeError("the image data is not base64"))
		}
		return vm.ToValue(vm.NewArrayBuffer(data))
	})
	_ = host.Set("axNew", func() int {
		id := len(p.ax) + 1
		p.ax[id] = newAXMemory()
		return id
	})
	_ = host.Set("axRead", func(id int, res goja.Value, diff bool) string {
		m := p.ax[id]
		if m == nil {
			m = newAXMemory()
			p.ax[id] = m
		}
		var or protocol.ObserveResult
		data, _ := json.Marshal(export(res))
		_ = json.Unmarshal(data, &or)
		return m.read(&or, diff)
	})
	_ = host.Set("axElement", func(id, el int) goja.Value {
		m := p.ax[id]
		if m == nil {
			return goja.Null()
		}
		e, ok := m.byID[el]
		if !ok {
			return goja.Null()
		}
		return vm.ToValue(map[string]any{"ref": e.Ref, "role": e.Role, "value": e.Value, "actions": e.Actions})
	})
	_ = host.Set("axWindow", func(id int) string {
		if m := p.ax[id]; m != nil {
			return m.window
		}
		return ""
	})
	_ = host.Set("guide", func() string { return replGuide })
	install, err := vm.RunScript("prelude.js", preludeJS)
	if err != nil {
		panic("prelude: " + err.Error())
	}
	fn, ok := goja.AssertFunction(install)
	if !ok {
		panic("prelude: not a function")
	}
	if _, err := fn(goja.Undefined(), host, vm.ToValue(r.h.Input())); err != nil {
		panic("prelude: " + err.Error())
	}
}

// render shows a value as text: strings as they are, everything else as
// indented JSON where it can be, else its string form.
func (p *REPL) render(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if goja.IsNull(v) {
		return "null"
	}
	if s, ok := v.Export().(string); ok {
		return s
	}
	vm := p.r.vm
	if o, ok := v.(*goja.Object); ok {
		if _, isFn := goja.AssertFunction(o); isFn {
			return v.String()
		}
		if b, ok := o.Export().([]byte); ok {
			return fmt.Sprintf("Uint8Array(%d)", len(b))
		}
		if j, ok := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify")); ok {
			if s, err := j(goja.Undefined(), v, goja.Null(), vm.ToValue(2)); err == nil && !goja.IsUndefined(s) {
				return s.String()
			}
		}
	}
	return v.String()
}

// emit adds an image from a data: URL, image bytes, or {bytes, mimeType}
// (also {data, mime} as observe returns it).
func (p *REPL) emit(v goja.Value) error {
	var data []byte
	mime := ""
	switch x := export(v).(type) {
	case string:
		switch {
		case strings.HasPrefix(x, "data:"):
			head, body, ok := strings.Cut(x[len("data:"):], ",")
			if !ok {
				return errors.New("emitImage: a data URL needs a comma before its data")
			}
			mime, _, _ = strings.Cut(head, ";")
			if strings.HasSuffix(head, ";base64") {
				b, err := base64.StdEncoding.DecodeString(body)
				if err != nil {
					return errors.New("emitImage: the data URL's base64 does not decode")
				}
				data = b
			} else {
				s, err := url.PathUnescape(body)
				if err != nil {
					return errors.New("emitImage: the data URL does not decode")
				}
				data = []byte(s)
			}
		case strings.HasPrefix(x, "file:"):
			return errors.New("emitImage: file URLs are refused: the REPL has no file system; pass the image's bytes or a data: URL")
		default:
			return errors.New("emitImage: a string must be a data: URL")
		}
	case goja.ArrayBuffer:
		data = x.Bytes()
	case []byte:
		data = x
	case map[string]any:
		if b, ok := bytesOf(x["bytes"]); ok {
			data = b
			mime, _ = x["mimeType"].(string)
		} else if s, ok := x["data"].(string); ok {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return errors.New("emitImage: data is not base64")
			}
			data = b
			mime, _ = x["mime"].(string)
		} else {
			return errors.New("emitImage: an object needs bytes (and mimeType)")
		}
	default:
		return errors.New("emitImage: pass a data: URL, image bytes, or {bytes, mimeType}")
	}
	if sniffed := sniffImage(data); sniffed != "" {
		mime = sniffed
	}
	switch mime {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return errors.New("emitImage: the image is not PNG, JPEG or WebP")
	}
	p.out.image(mime, data)
	return nil
}

func bytesOf(v any) ([]byte, bool) {
	switch b := v.(type) {
	case goja.ArrayBuffer:
		return b.Bytes(), true
	case []byte:
		return b, true
	}
	return nil, false
}

// sniffImage names PNG, JPEG and WebP data by their leading bytes.
func sniffImage(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
