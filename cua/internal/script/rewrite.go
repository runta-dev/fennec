package script

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/evanw/esbuild/pkg/api"
)

// replHead opens the async function a REPL cell runs in. It holds no
// newline, so a cell's line numbers are the wrapper's minus one.
const replHead = "(async function () { "

// prepared is a REPL cell ready to run.
type prepared struct {
	// Source is the script to run: an async function called at once, so
	// running it yields a promise of the cell's value.
	Source string
	// Globals are the names the cell declares at top level; they must
	// exist as global variables before Source runs.
	Globals []string
}

// prepareCell turns one REPL cell into a script with Node REPL semantics:
// top-level await works, top-level declarations (var, let, const,
// function, class) become global bindings that later cells see, and the
// value of a last expression statement is the cell's value.
//
// TypeScript annotations are stripped first. let and const become plain
// global variables, so a later cell may declare the same name again.
func prepareCell(code string) (prepared, error) {
	js, err := transpileCell(code)
	if err != nil {
		return prepared{}, err
	}
	src := replHead + "\n" + js + "\n})"
	prog, err := parser.ParseFile(nil, "cell.js", src, 0)
	if err != nil {
		return prepared{}, cellSyntaxError(err, src)
	}
	if len(prog.Body) != 1 {
		return prepared{}, errors.New("SyntaxError: the cell is not one program")
	}
	es, ok := prog.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return prepared{}, errors.New("SyntaxError: the cell is not one program")
	}
	fn, ok := es.Expression.(*ast.FunctionLiteral)
	if !ok || fn.Body == nil {
		return prepared{}, errors.New("SyntaxError: the cell closes its own wrapper")
	}
	// Offsets: goja's Idx is 1-based within src.
	type edit struct {
		start, end int
		text       string
	}
	var (
		edits   []edit
		globals []string
		hoist   []string
		seen    = map[string]bool{}
	)
	declare := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			globals = append(globals, name)
		}
	}
	text := func(n ast.Node) string { return src[int(n.Idx0())-1 : int(n.Idx1())-1] }
	list := fn.Body.List
	for i, st := range list {
		last := i == len(list)-1
		switch s := st.(type) {
		case *ast.VariableStatement:
			edits = append(edits, edit{int(s.Idx0()) - 1, int(s.Idx1()) - 1, assignments(s.List, false, text, declare)})
		case *ast.LexicalDeclaration:
			edits = append(edits, edit{int(s.Idx0()) - 1, int(s.Idx1()) - 1, assignments(s.List, true, text, declare)})
		case *ast.FunctionDeclaration:
			if s.Function.Name != nil {
				name := s.Function.Name.Name.String()
				declare(name)
				hoist = append(hoist, "globalThis["+quoteJS(name)+"] = "+name+";")
			}
		case *ast.ClassDeclaration:
			if s.Class.Name != nil {
				name := s.Class.Name.Name.String()
				declare(name)
				start, end := int(s.Idx0())-1, int(s.Idx1())-1
				edits = append(edits, edit{start, end, name + " = " + src[start:end] + ";"})
			}
		case *ast.ExpressionStatement:
			if last {
				// The expression's range leaves out parentheses around it:
				// the statement starts at the first of them and, being the
				// last, runs to the end of the cell.
				start := int(s.Idx0()) - 1
				for j := start - 1; j >= len(replHead); j-- {
					c := src[j]
					if c == '(' {
						start = j
						continue
					}
					if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
						break
					}
				}
				end := len(src) - len("\n})")
				for end > start && strings.ContainsRune(" \t\r\n;", rune(src[end-1])) {
					end--
				}
				edits = append(edits, edit{start, end, "return (" + src[start:end] + "\n)"})
			}
		}
	}
	sort.Slice(edits, func(a, b int) bool { return edits[a].start < edits[b].start })
	var b strings.Builder
	b.WriteString(replHead)
	b.WriteString(strings.Join(hoist, " "))
	pos := len(replHead)
	for _, e := range edits {
		if e.start < pos {
			continue // nested in an earlier edit; cannot happen at top level
		}
		b.WriteString(src[pos:e.start])
		b.WriteString(e.text)
		pos = e.end
	}
	b.WriteString(src[pos : len(src)-1]) // without the final ')'
	b.WriteString(")()")
	return prepared{Source: b.String(), Globals: globals}, nil
}

// assignments turns a declaration's bindings into one expression that
// assigns the same values to global variables. A let or const without an
// initializer sets undefined; a var without one keeps the value.
func assignments(list []*ast.Binding, lexical bool, text func(ast.Node) string, declare func(string)) string {
	var parts []string
	for _, bnd := range list {
		for _, n := range boundNames(bnd.Target) {
			declare(n)
		}
		target := text(bnd.Target)
		switch {
		case bnd.Initializer != nil:
			parts = append(parts, "("+target+" = "+text(bnd.Initializer)+")")
		case lexical:
			parts = append(parts, "("+target+" = undefined)")
		}
	}
	if len(parts) == 0 {
		return "void 0"
	}
	return strings.Join(parts, ", ")
}

// boundNames lists the identifiers a binding target declares.
func boundNames(t ast.Expression) []string {
	var out []string
	var walk func(e ast.Expression)
	walk = func(e ast.Expression) {
		switch x := e.(type) {
		case *ast.Identifier:
			out = append(out, x.Name.String())
		case *ast.ObjectPattern:
			for _, p := range x.Properties {
				switch q := p.(type) {
				case *ast.PropertyShort:
					out = append(out, q.Name.Name.String())
				case *ast.PropertyKeyed:
					walk(q.Value)
				}
			}
			if x.Rest != nil {
				walk(x.Rest)
			}
		case *ast.ArrayPattern:
			for _, el := range x.Elements {
				if el != nil {
					walk(el)
				}
			}
			if x.Rest != nil {
				walk(x.Rest)
			}
		case *ast.AssignExpression:
			walk(x.Left)
		case *ast.SpreadElement:
			walk(x.Expression)
		}
	}
	walk(t)
	return out
}

func quoteJS(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// transpileCell strips TypeScript and lowers what goja lacks (async
// generators), keeping the cell's top-level shape: no module wrapper, no
// bundling, no imports.
func transpileCell(code string) (string, error) {
	res := api.Transform(code, api.TransformOptions{
		Loader:     api.LoaderTS,
		Sourcefile: "cell.ts",
		Target:     api.ES2022,
		Supported:  map[string]bool{"async-generator": false, "for-await": false},
		LogLevel:   api.LogLevelSilent,
		// Keep names and statements as written: the rewrite reads them.
		TsconfigRaw: `{"compilerOptions":{"verbatimModuleSyntax":true,"useDefineForClassFields":true}}`,
	})
	if len(res.Errors) > 0 {
		var msgs []string
		for _, m := range res.Errors {
			loc := ""
			if m.Location != nil {
				loc = fmt.Sprintf("line %d:%d: ", m.Location.Line, m.Location.Column+1)
			}
			msgs = append(msgs, loc+m.Text)
		}
		return "", errors.New("SyntaxError: " + strings.Join(msgs, "; "))
	}
	return strings.TrimRight(string(res.Code), "\n"), nil
}

// cellSyntaxError names the line and column within the cell (the wrapper's
// first line does not count).
func cellSyntaxError(err error, src string) error {
	msg := err.Error()
	if el, ok := err.(parser.ErrorList); ok && len(el) > 0 {
		e := el[0]
		at := max(0, min(e.Offset, len(src)))
		before := src[:at]
		line := strings.Count(before, "\n")
		col := at - (strings.LastIndexByte(before, '\n') + 1) + 1
		msg = fmt.Sprintf("line %d:%d: %s", line, col, e.Message)
	}
	return errors.New("SyntaxError: " + msg)
}
