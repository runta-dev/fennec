package capability

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Promote turns "planned:<id>" into "<id>" in every catalog file of dir, for
// each id given. It refuses ids whose test function does not exist under
// root, and reports how many entries changed.
func Promote(dir, root string, ids []string) (int, error) {
	probe := Catalog{Files: []File{{Capabilities: []Capability{{ID: "probe", Tests: ids}}}}}
	if problems := CheckTests(probe, root); len(problems) > 0 {
		return 0, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return changed, err
		}
		text := string(data)
		for _, id := range ids {
			re := regexp.MustCompile(`planned:` + regexp.QuoteMeta(id) + `\b`)
			changed += len(re.FindAllStringIndex(text, -1))
			text = re.ReplaceAllString(text, id)
		}
		if text != string(data) {
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return changed, err
			}
		}
	}
	return changed, nil
}

// Progress counts written and planned tests and fully tested capabilities.
func Progress(c Catalog) (written, planned, done int) {
	for _, f := range c.Files {
		for _, cap := range f.Capabilities {
			all := true
			for _, t := range cap.Tests {
				if strings.HasPrefix(t, "planned:") {
					planned++
					all = false
				} else {
					written++
				}
			}
			if all {
				done++
			}
		}
	}
	return
}
