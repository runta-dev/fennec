// Package flows keeps saved parameterised flows: drafting a flow from a
// recording, the flow store (plain files with a git history where git is
// available, and an index), the chain of passing plans that later saves
// name, repairs of a replay that stopped, and finding a flow by intent.
package flows

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// MaxSlug is the longest flow name.
const MaxSlug = 40

var (
	placeholder  = regexp.MustCompile(`\{\{(\w+)(:base)?\}\}`)
	placeholders = regexp.MustCompile(`\{\{(\w+)`)
	slugRuns     = regexp.MustCompile(`[^a-z0-9]+`)
)

// norm lowers a name and drops ellipses, underscores and a trailing colon.
func norm(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("_", "", "…", "", "...", "").Replace(s)
	s = strings.TrimSuffix(s, ":")
	return strings.Join(strings.Fields(s), " ")
}

// Slug is a flow's file name for a name: lowercase, every run of other
// characters one dash, at most 40 characters.
func Slug(name string) string {
	s := slugRuns.ReplaceAllString(norm(name), "-")
	s = strings.Trim(s, "-")
	if len(s) > MaxSlug {
		s = s[:MaxSlug]
	}
	return s
}

// Base is the file name of a path, with either separator.
func Base(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Expand fills {{name}} and {{name:base}} placeholders from params; an
// unknown name stays as it is.
func Expand(s string, params map[string]string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		v, ok := params[sub[1]]
		if !ok {
			return m
		}
		if sub[2] != "" {
			return Base(v)
		}
		return v
	})
}

// Missing names the first placeholder of the steps that params do not fill.
func Missing(steps []protocol.Step, params map[string]string) string {
	for _, s := range steps {
		b, _ := json.Marshal(s)
		for _, m := range placeholder.FindAllStringSubmatch(string(b), -1) {
			if _, ok := params[m[1]]; !ok {
				return m[1]
			}
		}
	}
	return ""
}

// IsApp reports whether a step brings up an app (a chain starts there).
func IsApp(s protocol.Step) bool { return s.Launch != "" }

// Secure reports whether a step types a password: it is marked secret, or
// it must type into a secure field.
func Secure(s protocol.Step) bool {
	if s.Secret {
		return true
	}
	if s.Into == nil || s.Into.Select == "" {
		return false
	}
	sel, err := view.ParseSelector(s.Into.Select)
	if err != nil {
		return false
	}
	for _, st := range sel.States {
		if st == "protected" {
			return true
		}
	}
	return false
}

// Pinned reports whether a step's target may be pinned by a recorded
// fingerprint: not when a parameter changes its target or window.
func Pinned(s protocol.Step) bool {
	var texts []string
	for _, t := range []*protocol.Target{s.Press, s.Set, s.Click, s.Scroll, s.Drag, s.To, s.Perform, s.SelectText} {
		if t != nil {
			texts = append(texts, t.Select, t.Window)
		}
	}
	texts = append(texts, s.Menu, s.Window, s.Focus, s.Close)
	if s.Expect != nil {
		texts = append(texts, expectTexts(*s.Expect)...)
	}
	if s.Wait != nil {
		texts = append(texts, expectTexts(s.Wait.Expect)...)
	}
	for _, t := range texts {
		if strings.Contains(t, "{{") {
			return false
		}
	}
	return true
}

func expectTexts(e protocol.Expect) []string {
	out := []string{e.WindowOpens, e.WindowGone, e.Front}
	for _, t := range []*protocol.Target{e.Exists, e.Gone, e.Reads} {
		if t != nil {
			out = append(out, t.Select, t.Window)
		}
	}
	return out
}

// Fill returns the step with its placeholders filled from params. The
// recorded block keeps the recorded text (so replay can tell a target a
// parameter changed), a step whose target or window holds a parameter
// loses its recorded fingerprint, and a password step's intent is not
// filled.
func Fill(s protocol.Step, params map[string]string) (protocol.Step, error) {
	pinned := Pinned(s)
	secure := Secure(s)
	b, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return s, err
	}
	for k, v := range m {
		if k == "recorded" || (k == "intent" && secure) {
			continue
		}
		m[k] = expandAny(v, params)
	}
	b, err = json.Marshal(m)
	if err != nil {
		return s, err
	}
	var out protocol.Step
	if err := json.Unmarshal(b, &out); err != nil {
		return s, err
	}
	if !pinned && out.Recorded != nil {
		r := *out.Recorded
		r.Nth, r.Of = 0, 0
		out.Recorded = &r
	}
	return out, nil
}

// FillAll fills every step.
func FillAll(steps []protocol.Step, params map[string]string) ([]protocol.Step, error) {
	out := make([]protocol.Step, len(steps))
	for i, s := range steps {
		f, err := Fill(s, params)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

func expandAny(v any, params map[string]string) any {
	switch x := v.(type) {
	case string:
		return Expand(x, params)
	case map[string]any:
		for k, e := range x {
			x[k] = expandAny(e, params)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = expandAny(e, params)
		}
		return x
	}
	return v
}

// sortedKeys lists a map's keys in order, for a stable result.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cloneParams(p map[string]string) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

func cloneStep(s protocol.Step) protocol.Step {
	b, _ := json.Marshal(s)
	var out protocol.Step
	_ = json.Unmarshal(b, &out)
	return out
}

// templated turns the literal values steps type or set into parameters
// (each value under the name a param already gives it, else text, text_2,
// ... or the set target's slug), drops the intents of password steps, and
// templates window titles and addresses that hold a value (or a path
// value's file name, as {{name:base}}). It answers the steps and every
// parameter value, password values included.
func templated(steps []protocol.Step, params map[string]string) ([]protocol.Step, map[string]string) {
	p := cloneParams(params)
	used := map[string]bool{}
	for _, s := range steps {
		b, _ := json.Marshal(s)
		for _, m := range placeholders.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = true
		}
	}
	name := func(value, fallback string) string {
		for _, k := range sortedKeys(p) {
			if p[k] == value {
				return k
			}
		}
		n := fallback
		for i := 2; p[n] != "" || used[n] || hasKey(p, n); i++ {
			n = fallback + "_" + strconv.Itoa(i)
		}
		p[n] = value
		return n
	}
	out := make([]protocol.Step, len(steps))
	for i, s := range steps {
		t := cloneStep(s)
		if Secure(s) {
			t.Intent = ""
		}
		if t.Type != nil {
			v := *t.Type
			body := strings.TrimSuffix(v, "\n")
			if body != "" && !placeholder.MatchString(v) {
				n := name(body, "text")
				nv := "{{" + n + "}}" + v[len(body):]
				t.Type = &nv
			}
		}
		if t.Value != nil && t.Set != nil {
			var v string
			if json.Unmarshal(*t.Value, &v) == nil && v != "" && !placeholder.MatchString(v) {
				fallback := strings.ReplaceAll(Slug(selectorName(t.Set.Select)), "-", "_")
				if fallback == "" {
					fallback = "value"
				}
				raw, _ := json.Marshal("{{" + name(v, fallback) + "}}")
				rm := json.RawMessage(raw)
				t.Value = &rm
			}
		}
		out[i] = t
	}
	keys := sortedKeys(p)
	sub := func(v string) string {
		if v == "" {
			return v
		}
		for _, n := range keys {
			x := p[n]
			base := Base(x)
			if len(x) > 2 && strings.Contains(v, x) {
				v = strings.Replace(v, x, "{{"+n+"}}", 1)
			} else if strings.ContainsAny(x, `/\`) && len(base) > 2 && strings.Contains(v, base) {
				v = strings.Replace(v, base, "{{"+n+":base}}", 1)
			}
		}
		return v
	}
	for i := range out {
		t := &out[i]
		t.Window = sub(t.Window)
		t.Focus = sub(t.Focus)
		t.URL = sub(t.URL)
		if t.Web != nil {
			t.Web.URL = sub(t.Web.URL)
		}
		for _, e := range []*protocol.Expect{t.Expect, waitExpect(t.Wait)} {
			if e != nil {
				e.WindowOpens, e.WindowGone, e.Front = sub(e.WindowOpens), sub(e.WindowGone), sub(e.Front)
			}
		}
	}
	return out, p
}

func waitExpect(w *protocol.WaitRequest) *protocol.Expect {
	if w == nil {
		return nil
	}
	return &w.Expect
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// selectorName is the name part of a selector, or the selector itself.
func selectorName(s string) string {
	if sel, err := view.ParseSelector(s); err == nil && sel.Name != "" {
		return sel.Name
	}
	return s
}

// secretNames lists the parameters that password steps type.
func secretNames(steps []protocol.Step) map[string]bool {
	out := map[string]bool{}
	for _, s := range steps {
		if Secure(s) && s.Type != nil {
			for _, m := range placeholders.FindAllStringSubmatch(*s.Type, -1) {
				out[m[1]] = true
			}
		}
	}
	return out
}

// unsecret drops the values password steps type.
func unsecret(steps []protocol.Step, params map[string]string) map[string]string {
	out := cloneParams(params)
	for n := range secretNames(steps) {
		delete(out, n)
	}
	return out
}

// flowParams declares a saved flow's parameters: an example for each value,
// and each password as secret with no value.
func flowParams(steps []protocol.Step, values map[string]string) map[string]protocol.FlowParam {
	secret := secretNames(steps)
	out := map[string]protocol.FlowParam{}
	for k, v := range values {
		if !secret[k] {
			out[k] = protocol.FlowParam{Example: v}
		}
	}
	for k := range secret {
		out[k] = protocol.FlowParam{Secret: true}
	}
	return out
}
