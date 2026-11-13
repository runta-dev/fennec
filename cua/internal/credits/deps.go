package credits

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// A DEPS file is a Python literal program: assignments of dicts, lists,
// strings, booleans, Var()/Str() calls and string concatenation. This file
// reads that subset (what gclient's own evaluator accepts) and evaluates
// dependency conditions, without running any Python.

// dict is an ordered Python dict.
type dict struct {
	keys []string
	vals map[string]any
}

func (d *dict) get(k string) (any, bool) {
	if d == nil {
		return nil, false
	}
	v, ok := d.vals[k]
	return v, ok
}

// varRef is Var('name'): a placeholder expanded with the effective vars.
type varRef string

type tokKind int

const (
	tEOF tokKind = iota
	tName
	tStr
	tNum
	tOp
)

type token struct {
	kind tokKind
	s    string
	line int
}

func lex(src string) ([]token, error) {
	var out []token
	line := 1
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\\':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '\'' || c == '"' || ((c == 'r' || c == 'u' || c == 'b') && i+1 < len(src) && (src[i+1] == '\'' || src[i+1] == '"')):
			raw := c == 'r'
			if c != '\'' && c != '"' {
				i++
			}
			s, n, nl, err := lexString(src[i:], raw)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			out = append(out, token{tStr, s, line})
			line += nl
			i += n
		case c == '_' || unicode.IsLetter(rune(c)):
			j := i
			for j < len(src) && (src[j] == '_' || unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j]))) {
				j++
			}
			out = append(out, token{tName, src[i:j], line})
			i = j
		case c >= '0' && c <= '9' || c == '-' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			j := i + 1
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			out = append(out, token{tNum, src[i:j], line})
			i = j
		case strings.ContainsRune("{}[](),:=+.", rune(c)):
			out = append(out, token{tOp, string(c), line})
			i++
		default:
			return nil, fmt.Errorf("line %d: unexpected %q", line, c)
		}
	}
	return append(out, token{tEOF, "", line}), nil
}

// lexString reads one quoted string at the start of s; it answers the
// value, the bytes consumed and the newlines crossed.
func lexString(s string, raw bool) (string, int, int, error) {
	q := s[:1]
	if strings.HasPrefix(s, q+q+q) {
		q = q + q + q
	}
	var b strings.Builder
	nl := 0
	i := len(q)
	for {
		if i >= len(s) {
			return "", 0, 0, errors.New("unterminated string")
		}
		if strings.HasPrefix(s[i:], q) {
			return b.String(), i + len(q), nl, nil
		}
		c := s[i]
		if c == '\n' {
			if len(q) == 1 {
				return "", 0, 0, errors.New("newline in string")
			}
			nl++
		}
		if c == '\\' && i+1 < len(s) && !raw {
			i++
			switch e := s[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\', '\'', '"':
				b.WriteByte(e)
			case '\n':
				nl++
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }

func (p *parser) expect(op string) error {
	t := p.next()
	if t.kind != tOp || t.s != op {
		return fmt.Errorf("line %d: expected %q, found %q", t.line, op, t.s)
	}
	return nil
}

func (p *parser) isOp(op string) bool {
	t := p.peek()
	return t.kind == tOp && t.s == op
}

// parseDEPS evaluates a DEPS file to its top-level assignments.
func parseDEPS(src string) (map[string]any, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	out := map[string]any{}
	for p.peek().kind != tEOF {
		name := p.next()
		if name.kind != tName {
			return nil, fmt.Errorf("line %d: expected a name, found %q", name.line, name.s)
		}
		if err := p.expect("="); err != nil {
			return nil, err
		}
		v, err := p.expr()
		if err != nil {
			return nil, err
		}
		out[name.s] = v
	}
	return out, nil
}

func (p *parser) expr() (any, error) {
	v, err := p.atom()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") {
		p.next()
		w, err := p.atom()
		if err != nil {
			return nil, err
		}
		v, err = concat(v, w)
		if err != nil {
			return nil, err
		}
	}
	return v, nil
}

// concat adds two strings; a Var() operand becomes its "{name}" form.
func concat(a, b any) (any, error) {
	str := func(v any) (string, bool) {
		switch x := v.(type) {
		case string:
			return x, true
		case varRef:
			return "{" + string(x) + "}", true
		}
		return "", false
	}
	sa, ok1 := str(a)
	sb, ok2 := str(b)
	if !ok1 || !ok2 {
		return nil, errors.New("only strings can be added")
	}
	return sa + sb, nil
}

func (p *parser) atom() (any, error) {
	t := p.next()
	switch t.kind {
	case tStr:
		s := t.s
		for p.peek().kind == tStr { // implicit concatenation
			s += p.next().s
		}
		return s, nil
	case tNum:
		if n, err := strconv.ParseInt(t.s, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(t.s, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: bad number %q", t.line, t.s)
		}
		return f, nil
	case tName:
		switch t.s {
		case "True":
			return true, nil
		case "False":
			return false, nil
		case "None":
			return nil, nil
		}
		if !p.isOp("(") {
			return nil, fmt.Errorf("line %d: bare name %q", t.line, t.s)
		}
		p.next()
		var args []any
		for !p.isOp(")") {
			a, err := p.expr()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			if p.isOp(",") {
				p.next()
			}
		}
		p.next()
		switch t.s {
		case "Var":
			if len(args) != 1 {
				return nil, fmt.Errorf("line %d: Var takes one name", t.line)
			}
			n, _ := args[0].(string)
			return varRef(n), nil
		case "Str":
			if len(args) != 1 {
				return nil, fmt.Errorf("line %d: Str takes one string", t.line)
			}
			return args[0], nil
		}
		return nil, fmt.Errorf("line %d: unknown call %s()", t.line, t.s)
	case tOp:
		switch t.s {
		case "{":
			d := &dict{vals: map[string]any{}}
			for !p.isOp("}") {
				k, err := p.expr()
				if err != nil {
					return nil, err
				}
				ks, ok := k.(string)
				if !ok {
					return nil, fmt.Errorf("line %d: dict keys must be strings", t.line)
				}
				if err := p.expect(":"); err != nil {
					return nil, err
				}
				v, err := p.expr()
				if err != nil {
					return nil, err
				}
				if _, dup := d.vals[ks]; !dup {
					d.keys = append(d.keys, ks)
				}
				d.vals[ks] = v
				if p.isOp(",") {
					p.next()
				}
			}
			p.next()
			return d, nil
		case "[", "(":
			end := map[string]string{"[": "]", "(": ")"}[t.s]
			var l []any
			for !p.isOp(end) {
				v, err := p.expr()
				if err != nil {
					return nil, err
				}
				l = append(l, v)
				if p.isOp(",") {
					p.next()
				}
			}
			p.next()
			if t.s == "(" && len(l) == 1 {
				return l[0], nil
			}
			return l, nil
		}
	}
	return nil, fmt.Errorf("line %d: unexpected %q", t.line, t.s)
}

// Platform is the checkout the credits describe: gclient's built-in
// variables for an official macOS build of public Chromium.
type Platform struct {
	HostCPU string // gclient's host_cpu
	// TargetCPUs are the checkout_<cpu> variables set to true.
	TargetCPUs []string
}

// MacPlatform is the macOS checkout on an Apple silicon host, the only
// one Fennec ships. gclient's checkout variables for a macOS checkout
// carry both macOS CPUs (checkout_arm64 and checkout_x64), so DEPS
// conditions are evaluated with both set, as the official build's
// checkout does; the pinned credits hash depends on it.
func MacPlatform() Platform {
	return Platform{HostCPU: "arm64", TargetCPUs: []string{"arm64", "x64"}}
}

func (pl Platform) builtins() map[string]any {
	v := map[string]any{"host_os": "mac", "host_cpu": pl.HostCPU}
	for _, os := range []string{"android", "chromeos", "fuchsia", "ios", "linux", "mac", "win"} {
		v["checkout_"+os] = os == "mac"
	}
	for _, cpu := range []string{"arm", "arm64", "x86", "x64", "mips", "mips64", "ppc", "s390", "riscv64", "loong64"} {
		v["checkout_"+cpu] = false
	}
	for _, cpu := range pl.TargetCPUs {
		v["checkout_"+cpu] = true
	}
	return v
}

// Dep is one dependency a checkout contains.
type Dep struct {
	Path     string // relative to the source root ("v8", "third_party/skia")
	Type     string // git, cipd or gcs
	URL      string // git: the repository
	Revision string // git: the commit
	// Packages are CIPD packages ("name@version").
	Packages []string
	// Recurse says the dependency's own DEPS is read too.
	Recurse bool
}

// depsFile is one evaluated DEPS file.
type depsFile struct {
	vars        map[string]any
	deps        []Dep
	recurse     map[string]bool
	relative    bool
	unevaluated []string // conditions that referenced unknown variables
}

// expand substitutes {name} placeholders as Python's str.format does
// ("{{" and "}}" are literal braces); a variable's own placeholders are
// expanded too.
func expand(s string, vars map[string]any) (string, error) {
	return expandDepth(s, vars, 0)
}

func expandDepth(s string, vars map[string]any, depth int) (string, error) {
	if depth > 8 {
		return "", fmt.Errorf("variables nest too deeply in %q", s)
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '{' && i+1 < len(s) && s[i+1] == '{':
			b.WriteByte('{')
			i++
		case c == '}' && i+1 < len(s) && s[i+1] == '}':
			b.WriteByte('}')
			i++
		case c == '{':
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				return "", fmt.Errorf("unclosed placeholder in %q", s)
			}
			name := s[i+1 : i+j]
			v, ok := vars[name]
			if !ok {
				return "", fmt.Errorf("unknown variable %q", name)
			}
			vs := fmt.Sprint(v)
			if vb, ok := v.(bool); ok {
				vs = map[bool]string{true: "True", false: "False"}[vb]
			}
			if strings.ContainsAny(vs, "{}") {
				var err error
				if vs, err = expandDepth(vs, vars, depth+1); err != nil {
					return "", err
				}
			}
			b.WriteString(vs)
			i += j
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// evalDEPS reads one DEPS file. parentVars override the file's own
// (gclient's precedence); prefix is where its paths are rooted when it
// uses relative paths.
func evalDEPS(src string, parentVars map[string]any, pl Platform, prefix string) (*depsFile, error) {
	top, err := parseDEPS(src)
	if err != nil {
		return nil, err
	}
	f := &depsFile{vars: map[string]any{}, recurse: map[string]bool{}}
	if d, ok := top["vars"].(*dict); ok {
		for _, k := range d.keys {
			v := d.vals[k]
			if r, ok := v.(varRef); ok {
				v = "{" + string(r) + "}"
			}
			f.vars[k] = v
		}
	}
	for k, v := range parentVars {
		f.vars[k] = v
	}
	for k, v := range pl.builtins() {
		f.vars[k] = v
	}
	if b, ok := top["use_relative_paths"].(bool); ok {
		f.relative = b
	}
	if l, ok := top["recursedeps"].([]any); ok {
		for _, e := range l {
			switch x := e.(type) {
			case string:
				f.recurse[x] = true
			case []any:
				if len(x) > 0 {
					if s, ok := x[0].(string); ok {
						f.recurse[s] = true
					}
				}
			}
		}
	}
	deps, _ := top["deps"].(*dict)
	if deps == nil {
		return f, nil
	}
	for _, name := range deps.keys {
		v := deps.vals[name]
		dep := Dep{Type: "git", Recurse: f.recurse[name]}
		var url any
		switch x := v.(type) {
		case nil:
			continue
		case string, varRef:
			url = x
		case *dict:
			if c, ok := x.get("condition"); ok {
				cs, _ := c.(string)
				ok, err := evalCondition(cs, f.vars)
				if err != nil {
					f.unevaluated = append(f.unevaluated, name+": "+err.Error())
					continue
				}
				if !ok {
					continue
				}
			}
			if t, ok := x.get("dep_type"); ok {
				dep.Type, _ = t.(string)
			}
			url, _ = x.get("url")
			if pk, ok := x.get("packages"); ok {
				l, _ := pk.([]any)
				for _, e := range l {
					pd, _ := e.(*dict)
					n, _ := pd.get("package")
					ver, _ := pd.get("version")
					ns, _ := n.(string)
					vs, _ := ver.(string)
					if r, ok := ver.(varRef); ok {
						vs = "{" + string(r) + "}"
					}
					ns, err1 := expand(ns, f.vars)
					vs, err2 := expand(vs, f.vars)
					if err := errors.Join(err1, err2); err != nil {
						return nil, fmt.Errorf("%s: %w", name, err)
					}
					dep.Packages = append(dep.Packages, ns+"@"+vs)
				}
			}
		default:
			return nil, fmt.Errorf("%s: unexpected dependency value %T", name, v)
		}
		p := name
		if f.relative && prefix != "" {
			p = path.Join(prefix, name)
		}
		dep.Path = strings.TrimPrefix(path.Clean(p), "src/")
		if p == "src" {
			continue
		}
		if dep.Type == "git" {
			var us string
			switch x := url.(type) {
			case string:
				us = x
			case varRef:
				us = "{" + string(x) + "}"
			case nil:
				continue
			}
			us, err := expand(us, f.vars)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			at := strings.LastIndexByte(us, '@')
			if at < 0 {
				return nil, fmt.Errorf("%s: %q has no revision", name, us)
			}
			dep.URL, dep.Revision = strings.TrimSuffix(us[:at], ".git"), us[at+1:]
		}
		f.deps = append(f.deps, dep)
	}
	sort.Slice(f.deps, func(i, j int) bool { return f.deps[i].Path < f.deps[j].Path })
	return f, nil
}

// evalCondition evaluates a gclient condition: names, string literals,
// True/False, ==, !=, not, and, or, parentheses.
func evalCondition(src string, vars map[string]any) (bool, error) {
	toks, err := condTokens(src)
	if err != nil {
		return false, err
	}
	c := &cond{toks: toks, vars: vars}
	v, err := c.or()
	if err != nil {
		return false, err
	}
	if c.pos != len(c.toks) {
		return false, fmt.Errorf("trailing %q in condition %q", c.toks[c.pos], src)
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("condition %q is not a boolean", src)
	}
	return b, nil
}

func condTokens(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '(' || c == ')':
			out = append(out, string(c))
			i++
		case (c == '=' || c == '!') && i+1 < len(s) && s[i+1] == '=':
			out = append(out, s[i:i+2])
			i += 2
		case c == '\'' || c == '"':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				return nil, fmt.Errorf("unterminated string in %q", s)
			}
			out = append(out, s[i:i+j+2])
			i += j + 2
		case c == '_' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)):
			j := i
			for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		default:
			return nil, fmt.Errorf("unexpected %q in condition %q", c, s)
		}
	}
	return out, nil
}

type cond struct {
	toks  []string
	pos   int
	vars  map[string]any
	depth int
}

func (c *cond) peek() string {
	if c.pos < len(c.toks) {
		return c.toks[c.pos]
	}
	return ""
}

func (c *cond) or() (any, error) {
	v, err := c.and()
	for err == nil && c.peek() == "or" {
		c.pos++
		w, e := c.and()
		if e != nil {
			return nil, e
		}
		v = truthy(v) || truthy(w)
	}
	return v, err
}

func (c *cond) and() (any, error) {
	v, err := c.not()
	for err == nil && c.peek() == "and" {
		c.pos++
		w, e := c.not()
		if e != nil {
			return nil, e
		}
		v = truthy(v) && truthy(w)
	}
	return v, err
}

func (c *cond) not() (any, error) {
	if c.peek() == "not" {
		c.pos++
		v, err := c.not()
		return !truthy(v), err
	}
	return c.cmp()
}

func (c *cond) cmp() (any, error) {
	v, err := c.unit()
	if err != nil {
		return nil, err
	}
	if op := c.peek(); op == "==" || op == "!=" {
		c.pos++
		w, err := c.unit()
		if err != nil {
			return nil, err
		}
		eq := fmt.Sprint(v) == fmt.Sprint(w)
		return eq == (op == "=="), nil
	}
	return v, nil
}

func (c *cond) unit() (any, error) {
	t := c.peek()
	c.pos++
	switch {
	case t == "":
		return nil, errors.New("condition ends early")
	case t == "(":
		v, err := c.or()
		if err != nil {
			return nil, err
		}
		if c.peek() != ")" {
			return nil, errors.New("missing )")
		}
		c.pos++
		return v, nil
	case t == "True":
		return true, nil
	case t == "False":
		return false, nil
	case t[0] == '\'' || t[0] == '"':
		return t[1 : len(t)-1], nil
	}
	v, ok := c.vars[t]
	if !ok {
		// As gclient does, an unknown name is the string of its name
		// (host_os == mac).
		return t, nil
	}
	if s, ok := v.(string); ok {
		// A string variable is itself a condition, evaluated recursively
		// ("checkout_linux or checkout_mac"; "mac" stays "mac").
		if c.depth >= 8 {
			return nil, fmt.Errorf("variable %q nests too deeply", t)
		}
		toks, err := condTokens(s)
		if err != nil || len(toks) == 0 {
			return s, nil
		}
		sub := &cond{toks: toks, vars: c.vars, depth: c.depth + 1}
		r, err := sub.or()
		if err != nil || sub.pos != len(toks) {
			return s, nil
		}
		return r, nil
	}
	return v, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case nil:
		return false
	}
	return true
}
