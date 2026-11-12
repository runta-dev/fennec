// Package capability loads the capability catalog in tests/capabilities and
// checks that it accounts for every macOS behaviour of the pinned upstream.
package capability

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"fennec/cua/internal/upstream"
)

// Parity says how a capability is compared with shuttle.
type Parity string

const (
	// ParityOracle runs the same scenario against shuttle and fennec-cua and
	// compares outcomes.
	ParityOracle Parity = "oracle"
	// ParitySpec asserts fennec-cua's behaviour against the description only,
	// for behaviour shuttle exposes no observable surface for.
	ParitySpec Parity = "spec"
)

// Capability is one behaviour fennec-cua must provide on macOS.
type Capability struct {
	ID       string   `yaml:"id"`
	Title    string   `yaml:"title"`
	Behavior string   `yaml:"behavior"`
	Surface  string   `yaml:"surface"` // the fennec-cua tool or component that provides it
	Parity   Parity   `yaml:"parity"`
	Tests    []string `yaml:"tests"`    // fennec-cua test ids; "planned:<name>" until written
	Upstream []string `yaml:"upstream"` // inventory ids this capability accounts for
}

// Exclusion records an upstream macOS item fennec-cua deliberately does not
// reproduce, with the reason.
type Exclusion struct {
	Upstream []string `yaml:"upstream"`
	Reason   string   `yaml:"reason"`
}

// File is one domain file of the catalog.
type File struct {
	Domain       string       `yaml:"domain"`
	Summary      string       `yaml:"summary"`
	Capabilities []Capability `yaml:"capabilities"`
	Excluded     []Exclusion  `yaml:"excluded"`

	path string
}

// Catalog is every domain file.
type Catalog struct {
	Files []File
}

var (
	capID    = regexp.MustCompile(`^CAP-[A-Z]+-\d{3}$`)
	testID   = regexp.MustCompile(`^(planned:)?[A-Za-z0-9_./:-]+$`)
	surfaces = map[string]bool{"observe": true, "act": true, "wait": true, "run": true, "flow": true, "status": true, "daemon": true, "policy": true, "mcp": true, "models": true, "extensions": true, "packaging": true}
	parities = map[Parity]bool{ParityOracle: true, ParitySpec: true}
)

// Load reads every *.yaml file of dir.
func Load(dir string) (Catalog, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return Catalog{}, err
	}
	sort.Strings(paths)
	var c Catalog
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return Catalog{}, err
		}
		var f File
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil {
			return Catalog{}, fmt.Errorf("%s: %w", path, err)
		}
		f.path = path
		c.Files = append(c.Files, f)
	}
	return c, nil
}

// Report is the outcome of a coverage check.
type Report struct {
	Capabilities int
	Planned      int // capabilities without a written test yet
	Covered      int // macOS items a capability accounts for
	Excluded     int // macOS items an exclusion accounts for
	Missing      []upstream.Item
	Problems     []string
}

// OK reports whether the catalog is valid and complete.
func (r Report) OK() bool { return len(r.Missing) == 0 && len(r.Problems) == 0 }

// Check validates the catalog and its coverage of items.
func Check(c Catalog, items []upstream.Item) Report {
	var r Report
	byID := map[string]upstream.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	ids := map[string]string{}
	claimed := map[string]string{}
	claim := func(where, ref string) {
		it, ok := byID[ref]
		switch {
		case !ok:
			r.Problems = append(r.Problems, fmt.Sprintf("%s: unknown upstream id %q", where, ref))
		case !it.MacOS:
			r.Problems = append(r.Problems, fmt.Sprintf("%s: %q is not a macOS item (%s)", where, ref, it.Why))
		case claimed[ref] != "" && claimed[ref] != where:
			// Several capabilities may share an item; that is fine.
		}
		if ok && claimed[ref] == "" {
			claimed[ref] = where
		}
	}
	domains := map[string]string{}
	for _, f := range c.Files {
		name := filepath.Base(f.path)
		if f.Domain == "" || f.Summary == "" {
			r.Problems = append(r.Problems, name+": domain and summary are required")
		}
		if prev := domains[f.Domain]; prev != "" {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: domain %q already used by %s", name, f.Domain, prev))
		}
		domains[f.Domain] = name
		for _, cap := range f.Capabilities {
			where := name + ":" + cap.ID
			r.Capabilities++
			switch {
			case !capID.MatchString(cap.ID):
				r.Problems = append(r.Problems, where+": id must look like CAP-XXX-001")
			case ids[cap.ID] != "":
				r.Problems = append(r.Problems, fmt.Sprintf("%s: id already used in %s", where, ids[cap.ID]))
			}
			ids[cap.ID] = name
			if cap.Title == "" || cap.Behavior == "" {
				r.Problems = append(r.Problems, where+": title and behavior are required")
			}
			if !surfaces[cap.Surface] {
				r.Problems = append(r.Problems, fmt.Sprintf("%s: unknown surface %q", where, cap.Surface))
			}
			if !parities[cap.Parity] {
				r.Problems = append(r.Problems, fmt.Sprintf("%s: parity must be oracle or spec", where))
			}
			if len(cap.Tests) == 0 {
				r.Problems = append(r.Problems, where+": at least one test (or planned:<name>) is required")
			}
			planned := false
			for _, t := range cap.Tests {
				if !testID.MatchString(t) {
					r.Problems = append(r.Problems, fmt.Sprintf("%s: malformed test id %q", where, t))
				}
				if strings.HasPrefix(t, "planned:") {
					planned = true
				}
			}
			if planned {
				r.Planned++
			}
			for _, ref := range cap.Upstream {
				claim(where, ref)
			}
		}
		for i, ex := range f.Excluded {
			where := fmt.Sprintf("%s:excluded[%d]", name, i)
			if strings.TrimSpace(ex.Reason) == "" {
				r.Problems = append(r.Problems, where+": reason is required")
			}
			for _, ref := range ex.Upstream {
				claim(where, ref)
			}
		}
	}
	for _, it := range items {
		if !it.MacOS {
			continue
		}
		where := claimed[it.ID]
		switch {
		case where == "":
			r.Missing = append(r.Missing, it)
		case strings.Contains(where, ":excluded["):
			r.Excluded++
		default:
			r.Covered++
		}
	}
	return r
}
