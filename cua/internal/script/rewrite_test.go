package script

import (
	"strings"
	"testing"
)

func TestPrepareCellDeclarations(t *testing.T) {
	p, err := prepareCell("let a = 1, {b, c: [d]} = o\nconst e = await f()\nvar g\nfunction h() { return a }\nclass K extends Base {}\nlet z: number\nh()")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Globals, ","); got != "a,b,d,e,g,h,K,z" {
		t.Errorf("globals %s", got)
	}
	for _, want := range []string{`globalThis["h"] = h;`, "(a = 1), ({ b, c: [d] } = o)", "(e = await f())", "void 0", "K = class K extends Base", "(z = undefined)", "return (h()\n)"} {
		if !strings.Contains(p.Source, want) {
			t.Errorf("source lacks %q:\n%s", want, p.Source)
		}
	}
}

func TestPrepareCellSyntaxError(t *testing.T) {
	_, err := prepareCell("let x = \n  (")
	if err == nil || !strings.HasPrefix(err.Error(), "SyntaxError:") {
		t.Fatalf("err %v", err)
	}
}
