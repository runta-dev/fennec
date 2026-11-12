package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"fennec/cua/internal/flows"
	"fennec/cua/internal/protocol"
)

// Keys of a step by the JSON type they take.
var (
	textKeys   = map[string]bool{"key": true, "menu": true, "focus": true, "close": true, "launch": true, "url": true, "app": true, "window": true, "input": true, "intent": true, "button": true, "direction": true, "type": true}
	targetKeys = map[string]bool{"press": true, "set": true, "click": true, "scroll": true, "drag": true, "to": true, "into": true}
	objectKeys = map[string]bool{"wait": true, "expect": true, "web": true, "recorded": true}
	boolKeys   = map[string]bool{"secret": true, "any_focus": true}
	numberKeys = map[string]bool{"count": true, "amount": true, "pid": true}
	opKeys     = []string{"press", "set", "click", "type", "key", "scroll", "drag", "menu", "focus", "close", "launch", "wait"}
)

// CheckRaw checks an act call's steps as sent, before anything is decoded
// or done: every step an object of known keys, each text key a string,
// each target a string or object, the flags booleans. A null counts as
// absent. The error names the step and the key.
func CheckRaw(args json.RawMessage) *protocol.Error {
	t := bytes.TrimSpace(args)
	if len(t) == 0 || t[0] != '{' {
		return nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(t, &top) != nil {
		return nil
	}
	raw, ok := top["steps"]
	if !ok || isNull(raw) {
		return nil
	}
	var steps []json.RawMessage
	if err := json.Unmarshal(raw, &steps); err != nil {
		return &protocol.Error{Code: protocol.ErrInvalid, Message: "steps must be a list of step objects; nothing ran"}
	}
	for i, s := range steps {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(s, &m); err != nil || m == nil {
			return stepErr(i, "", "is not an object")
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := bytes.TrimSpace(m[k])
			if isNull(v) {
				continue
			}
			switch {
			case textKeys[k]:
				if v[0] != '"' {
					if k == "close" && string(v) == "false" {
						return stepErr(i, k, "is false, which closes nothing")
					}
					return stepErr(i, k, "must be text, not "+jsonKind(v))
				}
			case targetKeys[k]:
				if v[0] != '"' && v[0] != '{' {
					return stepErr(i, k, "must be a ref, a selector or a target object, not "+jsonKind(v))
				}
			case objectKeys[k]:
				if v[0] != '{' {
					return stepErr(i, k, "must be an object, not "+jsonKind(v))
				}
			case boolKeys[k]:
				if string(v) != "true" && string(v) != "false" {
					return stepErr(i, k, "must be true or false, not "+jsonKind(v))
				}
			case numberKeys[k]:
				if !strings.ContainsAny(string(v[:1]), "-0123456789") {
					return stepErr(i, k, "must be a number, not "+jsonKind(v))
				}
			case k == "args" || k == "mods":
				var list []string
				if json.Unmarshal(v, &list) != nil {
					return stepErr(i, k, "must be a list of text")
				}
			case k == "value":
			default:
				return stepErr(i, k, "is not a step key")
			}
		}
	}
	return nil
}

func isNull(v json.RawMessage) bool { return string(bytes.TrimSpace(v)) == "null" }

func jsonKind(v json.RawMessage) string {
	switch v[0] {
	case '"':
		return "text"
	case '{':
		return "an object"
	case '[':
		return "a list"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	}
	return "a number"
}

func stepErr(i int, key, why string) *protocol.Error {
	msg := fmt.Sprintf("step %d", i)
	if key != "" {
		msg += ": " + key
	}
	return &protocol.Error{Code: protocol.ErrInvalid, Message: msg + " " + why + "; nothing ran", Hint: "fix the step and send the plan again"}
}

// Validate checks the whole plan before anything is sent: one operation
// per step with a value, every placeholder filled, password steps one line.
// The same check runs before a flow is saved from steps.
func Validate(steps []protocol.Step, params map[string]string) *protocol.Error {
	if len(steps) == 0 {
		return &protocol.Error{Code: protocol.ErrInvalid, Message: "act needs at least one step"}
	}
	for i, s := range steps {
		if perr := validateStep(i, s); perr != nil {
			return perr
		}
		if name := flows.Missing([]protocol.Step{s}, params); name != "" {
			return &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("step %d: no value for {{%s}}; nothing ran", i, name), Hint: "pass it in params"}
		}
	}
	return nil
}

func validateStep(i int, s protocol.Step) *protocol.Error {
	kind, err := s.Kind()
	if err != nil {
		var set []string
		for _, k := range opKeys {
			if hasOp(s, k) {
				set = append(set, k)
			}
		}
		if len(set) > 1 {
			return stepErr(i, strings.Join(set, " and "), "are two operations; a step has one")
		}
		return stepErr(i, "", "has no operation (or an empty one) and no expect")
	}
	emptyTarget := func(t *protocol.Target) bool {
		return t == nil || (t.Ref == "" && strings.TrimSpace(t.Select) == "" && t.Point == nil)
	}
	switch kind {
	case "press", "set", "click", "scroll", "drag":
		var t *protocol.Target
		switch kind {
		case "press":
			t = s.Press
		case "set":
			t = s.Set
		case "click":
			t = s.Click
		case "scroll":
			t = s.Scroll
		case "drag":
			t = s.Drag
		}
		if emptyTarget(t) {
			return stepErr(i, kind, "is empty")
		}
		if kind == "drag" && emptyTarget(s.To) {
			return stepErr(i, "to", "is missing; a drag needs where it ends")
		}
		if kind == "set" && s.Value == nil {
			return stepErr(i, "value", "is missing; set needs the value to give")
		}
	case "key":
		if strings.TrimSpace(s.Key) == "" {
			return stepErr(i, "key", "is empty")
		}
	case "type":
		if s.Into != nil && s.Into.Point != nil && *s.Type == "" {
			return stepErr(i, "type", "is empty: a type at a point needs text")
		}
	case "wait":
		if emptyWait(*s.Wait) && s.Wait.TimeoutMs == 0 {
			return stepErr(i, "wait", "names no condition")
		}
	case "expect":
		if emptyExpect(*s.Expect) {
			return stepErr(i, "expect", "names no condition")
		}
	}
	if s.Expect != nil && kind != "expect" && emptyExpect(*s.Expect) {
		return stepErr(i, "expect", "names no condition")
	}
	if s.Secret {
		if kind != "type" {
			return stepErr(i, "secret", "marks a "+kind+" step; only a type step carries a password")
		}
		body := strings.TrimSuffix(*s.Type, "\n")
		if body == "" || strings.ContainsAny(body, "\r\n") {
			return stepErr(i, "secret", "needs one password line, optionally ending in a newline")
		}
	}
	return nil
}

func hasOp(s protocol.Step, k string) bool {
	switch k {
	case "press":
		return s.Press != nil
	case "set":
		return s.Set != nil
	case "click":
		return s.Click != nil
	case "type":
		return s.Type != nil
	case "key":
		return s.Key != ""
	case "scroll":
		return s.Scroll != nil
	case "drag":
		return s.Drag != nil
	case "menu":
		return s.Menu != ""
	case "focus":
		return s.Focus != ""
	case "close":
		return s.Close != ""
	case "launch":
		return s.Launch != ""
	case "wait":
		return s.Wait != nil
	}
	return false
}

func emptyExpect(e protocol.Expect) bool {
	return e.WindowOpens == "" && e.WindowGone == "" && e.Front == "" && e.Exists == nil && e.Gone == nil && e.Reads == nil && e.Looks == ""
}

func emptyWait(w protocol.WaitRequest) bool {
	return emptyExpect(w.Expect) && !w.Still && !w.Changed
}
