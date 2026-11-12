package capability

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var testFunc = regexp.MustCompile(`(?m)^func (Test\w+)\(`)

// CheckTests verifies that every written (non-planned) test id names a Go
// test function that exists: "internal/view.TestOutline" must be a func
// TestOutline in a _test.go file of root/internal/view.
func CheckTests(c Catalog, root string) []string {
	found := map[string]map[string]bool{}
	scan := func(pkg string) map[string]bool {
		if m, ok := found[pkg]; ok {
			return m
		}
		m := map[string]bool{}
		files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pkg), "*_test.go"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, mm := range testFunc.FindAllSubmatch(data, -1) {
				m[string(mm[1])] = true
			}
		}
		found[pkg] = m
		return m
	}
	var problems []string
	for _, f := range c.Files {
		for _, cap := range f.Capabilities {
			for _, t := range cap.Tests {
				if strings.HasPrefix(t, "planned:") || strings.HasPrefix(t, "scenario/") {
					continue
				}
				pkg, name, ok := strings.Cut(t, ".Test")
				if !ok {
					problems = append(problems, fmt.Sprintf("%s: test id %q is not <package>.Test<Name>", cap.ID, t))
					continue
				}
				if !scan(pkg)["Test"+name] {
					problems = append(problems, fmt.Sprintf("%s: test %s does not exist", cap.ID, t))
				}
			}
		}
	}
	return problems
}
