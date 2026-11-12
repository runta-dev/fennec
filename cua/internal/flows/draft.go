package flows

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/record"
	"fennec/cua/internal/view"
)

// CaptionsAtOnce bounds the model captions a draft asks for at once.
const CaptionsAtOnce = 4

// maxCaption is the longest caption a draft keeps.
const maxCaption = 80

// CaptionRequest is a click a draft needs a name for: its crop, the crop's
// pixels per desktop unit, the click's offset from the crop centre and the
// element's role, if any.
type CaptionRequest struct {
	Crop   string
	Scale  float64
	Offset [2]float64
	Role   string
}

// Captioner names a clicked control from its crop (one model ask).
type Captioner func(ctx context.Context, c CaptionRequest) (string, error)

// keyNames maps macOS virtual key codes to the key-name vocabulary.
var keyNames = map[int]string{
	36: "return", 76: "return", 48: "tab", 53: "escape", 51: "backspace", 117: "forward_delete",
	123: "left", 124: "right", 125: "down", 126: "up", 115: "home", 119: "end", 116: "pageup", 121: "pagedown",
	122: "f1", 120: "f2", 99: "f3", 118: "f4", 96: "f5", 97: "f6", 98: "f7", 100: "f8", 101: "f9", 109: "f10", 103: "f11", 111: "f12",
}

// namedRoles are the roles a click step names along with the element's name.
var namedRoles = map[string]bool{"button": true, "checkbox": true, "radio": true, "tab": true, "link": true, "switch": true, "togglebutton": true, "menuitem": true}

// CleanCaption keeps a model caption's first line, at most 80 characters,
// without trailing periods or quotes, so text after the first line never
// enters a plan.
func CleanCaption(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > maxCaption {
		s = string(r[:maxCaption])
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, `."`)
	s = strings.TrimLeft(s, `"`)
	return strings.TrimSpace(s)
}

// own is a click's own element name, when it has a usable one.
func own(e record.Event) string {
	if e.Root || e.Element == nil {
		return ""
	}
	return e.Element.Name
}

func windowTitle(e record.Event) string {
	if e.Kind == record.KindClick && e.Window != nil {
		return e.Window.Title
	}
	if e.Focus != nil {
		return e.Focus.Window
	}
	return ""
}

func pageOf(e record.Event) string {
	if e.Web != nil && e.Web.URL != "" {
		return e.Web.URL
	}
	if e.Focus != nil {
		return e.Focus.URL
	}
	return ""
}

// fieldSelector names the field a typed run went to; secure requires a
// password field, and no field read leaves any password field.
func fieldSelector(f *record.Field, secure bool) string {
	sel := view.Selector{}
	if f != nil {
		if f.Role != "" {
			sel.Roles = []string{f.Role}
		}
		sel.Name = f.Name
		secure = secure || f.Secure
	}
	if secure {
		sel.States = []string{"protected"}
	}
	if sel.Name == "" && len(sel.Roles) == 0 {
		if secure {
			return "secure"
		}
		return ""
	}
	return sel.String()
}

// Draft turns a recording into a draft plan. params names values the user
// typed or clicked (name to value); each becomes {{name}} wherever it shows.
func Draft(ctx context.Context, rec *record.Recording, params map[string]string, caption Captioner) (*protocol.Flow, error) {
	type example struct{ name, value string }
	var examples []example
	for k, v := range params {
		if v != "" {
			examples = append(examples, example{k, v})
		}
	}
	sort.Slice(examples, func(i, j int) bool {
		if len(examples[i].value) != len(examples[j].value) {
			return len(examples[i].value) > len(examples[j].value)
		}
		return examples[i].name < examples[j].name
	})
	d := &protocol.Flow{Name: rec.ID, Params: map[string]protocol.FlowParam{}, Steps: []protocol.Step{}, Recording: rec.Path}
	for _, x := range examples {
		d.Params[x.name] = protocol.FlowParam{Example: x.value}
	}
	templ := func(s string) string {
		for _, x := range examples {
			s = strings.ReplaceAll(s, x.value, "{{"+x.name+"}}")
		}
		return s
	}
	events := rec.Events
	captions := captionAll(ctx, events, caption)

	push := func(s protocol.Step) { d.Steps = append(d.Steps, s) }
	last := func() *protocol.Step {
		if len(d.Steps) == 0 {
			return nil
		}
		return &d.Steps[len(d.Steps)-1]
	}

	// The focus of the run being typed: unset, read as none, or a field.
	type focus struct {
		set   bool
		field *record.Field
	}
	var (
		typed    string
		secret   bool
		filled   bool
		secrets  int
		into     focus
		clickedE = -1
		clickedS = -1
	)
	applyFocus := func(s *protocol.Step, f focus, secure bool) {
		switch {
		case secure:
			s.Into = &protocol.Target{Select: fieldSelector(f.field, true)}
		case f.set && f.field == nil:
			s.AnyFocus = true
		case f.field != nil:
			if sel := fieldSelector(f.field, false); sel != "" {
				s.Into = &protocol.Target{Select: sel}
			}
		}
		if f.field != nil && f.field.URL != "" {
			s.Web = &protocol.WebCheck{URL: templ(f.field.URL), DOMID: f.field.ID}
		}
	}
	password := func(f *record.Field) {
		name := "password"
		if secrets > 0 {
			name = "password_" + strconv.Itoa(secrets+1)
		}
		secrets++
		d.Params[name] = protocol.FlowParam{Secret: true}
		text := "{{" + name + "}}"
		s := protocol.Step{Type: &text, Secret: true, Intent: fmt.Sprintf("type the %s into the password field (not recorded: pass it as %s)", strings.ReplaceAll(name, "_", " "), name)}
		applyFocus(&s, focus{set: f != nil, field: f}, true)
		push(s)
	}
	flush := func() {
		if secret {
			if !filled {
				password(into.field)
			}
		} else if typed != "" {
			text := templ(typed)
			s := protocol.Step{Type: &text}
			if text == typed {
				s.Intent = "type " + strconv.Quote(typed)
			} else {
				s.Intent = "type " + text
			}
			applyFocus(&s, into, false)
			push(s)
		}
		typed, secret, into = "", false, focus{}
	}
	at := func(app, page, title, verb string) protocol.Step {
		if page != "" {
			return protocol.Step{Launch: app, URL: templ(page), Intent: fmt.Sprintf("%s %s in %s", verb, page, app)}
		}
		s := protocol.Step{Launch: app, Window: templ(title), Intent: verb + " " + app}
		if title != "" {
			s.Intent += fmt.Sprintf(" at %q", title)
		}
		return s
	}

	var first *record.Event
	app := ""
	for i := range events {
		e := events[i]
		if e.Kind != record.KindClick && e.Kind != record.KindKey {
			continue
		}
		if first == nil {
			first = &events[i]
		}
		if app == "" && e.App != "" {
			app = e.App
		}
	}
	opened := ""
	if first != nil {
		opened = windowTitle(*first)
	}
	if app != "" {
		page := ""
		if first != nil {
			page = pageOf(*first)
		}
		push(at(app, page, opened, "open"))
	}
	here := struct{ app, window string }{app, opened}
	move := func(e record.Event) {
		w := windowTitle(e)
		if e.App == "" {
			return
		}
		if e.App == here.app && (w == "" || here.window == "" || w == here.window) {
			if w != "" {
				here.window = w
			}
			return
		}
		flush()
		here.app, here.window = e.App, w
		push(at(e.App, pageOf(e), w, "switch to"))
	}
	seen := map[string]bool{}

	for i, e := range events {
		if e.Kind == record.KindKey || e.Kind == record.KindClick {
			move(e)
		}
		switch e.Kind {
		case record.KindKey:
			if e.Secret {
				if typed != "" {
					flush()
				}
				if !secret && e.Focus != nil {
					into = focus{set: true, field: e.Focus}
				}
				secret = true
				continue
			}
			code := -1
			if e.Code != nil {
				code = *e.Code
			}
			named := keyNames[code]
			if onlyShiftAlt(e.Mods) && named == "" && e.Text != "" {
				if secret {
					flush()
				}
				filled = false
				if typed == "" && e.FocusRead {
					into = focus{set: true, field: e.Focus}
				}
				typed += e.Text
				continue
			}
			flush()
			filled = false
			k := named
			if k == "" {
				k = strings.ToLower(e.Text)
			}
			combo := strings.Join(append(append([]string(nil), e.Mods...), k), "+")
			push(protocol.Step{Key: combo, Intent: "press " + combo})
			continue
		case record.KindNote:
			d.Notes = append(d.Notes, protocol.FlowNote{Before: len(d.Steps), Text: e.Text})
			continue
		}
		flush()
		switch e.Kind {
		case record.KindPassword:
			if !filled {
				password(e.Focus)
			} else if l := last(); l != nil && e.Focus != nil && l.Type != nil && l.Into != nil && l.Into.Select == "secure" {
				// The click into the field named no field: the focus does.
				applyFocus(l, focus{set: true, field: e.Focus}, true)
			}
			filled = true
		case record.KindSkipped:
			d.Gaps = append(d.Gaps, len(d.Steps))
		case record.KindAfter:
			w := ""
			if e.Window != nil {
				w = e.Window.Title
			}
			if l := last(); w != "" && l != nil && !IsApp(*l) && !seen[w] && e.Role == "window" {
				if l.Expect == nil {
					l.Expect = &protocol.Expect{}
				}
				l.Expect.Front = templ(w)
			}
			if w != "" {
				seen[w] = true
				if e.App != "" && e.Role == "window" {
					here.app, here.window = e.App, w
				}
			}
		case record.KindClick:
			role := ""
			if e.Element != nil {
				role = e.Element.Role
			}
			if role == "menu" {
				if n := nextClick(events, i); n != nil && n.Element != nil && n.Element.Role == "menuitem" {
					continue
				}
			}
			if role == "menuitem" {
				if p := prevClick(events, i); p != nil && p.Element != nil && p.Element.Role == "menu" {
					menu := p.Element.Name + " > " + e.Element.Name
					push(protocol.Step{Menu: menu, Intent: "choose " + menu})
					continue
				}
			}
			if e.Count > 1 && clickedE >= 0 && clickedS == len(d.Steps)-1 && near(events[clickedE], e, 4) {
				d.Steps = d.Steps[:len(d.Steps)-1]
			}
			push(clickStep(e, templ, captions[i]))
			clickedE, clickedS = i, len(d.Steps)-1
			filled = e.Secret
			if filled {
				password(nil)
			}
		}
	}
	flush()
	return d, nil
}

func onlyShiftAlt(mods []string) bool {
	for _, m := range mods {
		if m != "shift" && m != "alt" {
			return false
		}
	}
	return true
}

func nextClick(events []record.Event, i int) *record.Event {
	for j := i + 1; j < len(events); j++ {
		if events[j].Kind == record.KindClick {
			return &events[j]
		}
	}
	return nil
}

func prevClick(events []record.Event, i int) *record.Event {
	for j := i - 1; j >= 0; j-- {
		if events[j].Kind == record.KindClick {
			return &events[j]
		}
	}
	return nil
}

func near(a, b record.Event, d float64) bool {
	if a.X == nil || a.Y == nil || b.X == nil || b.Y == nil {
		return false
	}
	return math.Hypot(*a.X-*b.X, *a.Y-*b.Y) <= d
}

// captionAll asks for the captions of every click with a crop and no
// usable name, CaptionsAtOnce at a time; a failed caption is empty.
func captionAll(ctx context.Context, events []record.Event, caption Captioner) map[int]string {
	out := map[int]string{}
	if caption == nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, CaptionsAtOnce)
	for i, e := range events {
		if e.Kind != record.KindClick || e.Crop == "" || own(e) != "" {
			continue
		}
		req := CaptionRequest{Crop: e.Crop, Scale: e.CropScale}
		if req.Scale == 0 {
			req.Scale = 1
		}
		if e.CropOffset != nil {
			req.Offset = *e.CropOffset
		}
		if e.Element != nil {
			req.Role = e.Element.Role
		}
		wg.Add(1)
		go func(i int, req CaptionRequest) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			text, err := caption(ctx, req)
			if err != nil {
				return
			}
			mu.Lock()
			out[i] = CleanCaption(text)
			mu.Unlock()
		}(i, req)
	}
	wg.Wait()
	return out
}

// clickStep is a recorded click: its target by name (with its role for a
// named control) or by caption, its button, count and modifiers, its page,
// and the recorded block replay falls back on.
func clickStep(e record.Event, templ func(string) string, captioned string) protocol.Step {
	named := own(e)
	role := ""
	if e.Element != nil {
		role = e.Element.Role
	}
	said := named
	if said == "" {
		said = captioned
	}
	if said == "" {
		what := role
		if what == "" {
			what = "point"
		}
		x, y := 0.0, 0.0
		if e.X != nil && e.Y != nil {
			x, y = *e.X, *e.Y
		}
		said = fmt.Sprintf("the %s at %d, %d", what, int(math.Round(x)), int(math.Round(y)))
	}
	target := templ(said)
	sel := view.Selector{Name: target}
	if named != "" && namedRoles[role] {
		sel.Roles = []string{role}
	}
	rec := &protocol.Recorded{Text: said, Root: e.Root}
	if e.Crop != "" {
		rec.Crop, rec.Scale = e.Crop, e.CropScale
		if rec.Scale == 0 {
			rec.Scale = 1
		}
		if o := e.CropOffset; o != nil && (o[0] != 0 || o[1] != 0) {
			off := *o
			rec.Offset = &off
		}
	}
	if e.Window != nil && e.Window.Bounds != nil && e.X != nil && e.Y != nil {
		b := e.Window.Bounds
		rec.At = &[2]float64{math.Round(*e.X - float64(b[0])), math.Round(*e.Y - float64(b[1]))}
		rec.Size = &[2]float64{float64(b[2]), float64(b[3])}
	}
	s := protocol.Step{Click: &protocol.Target{Select: sel.String()}, Recorded: rec}
	if e.Button != "" && e.Button != "left" {
		s.Button = e.Button
	}
	if e.Count > 1 {
		s.Count = e.Count
	}
	if len(e.Mods) > 0 {
		s.Mods = append([]string(nil), e.Mods...)
	}
	if e.Web != nil && e.Web.URL != "" {
		s.Web = &protocol.WebCheck{URL: templ(e.Web.URL), DOMID: e.Web.ID}
	}
	if named != "" {
		s.Intent = fmt.Sprintf("click the %s %q", role, named)
	} else {
		s.Intent = "click " + said
	}
	if e.Window != nil && e.Window.Title != "" {
		s.Intent += fmt.Sprintf(" in %q", e.Window.Title)
	}
	return s
}
