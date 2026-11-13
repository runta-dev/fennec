package bench

import (
	"fmt"
	"strings"
)

// Correction repairs a task whose upstream grader or pre_command is broken
// in a way no agent can get past. Each one is a literal text replacement, so
// the repair is visible in this table and fails loudly when upstream changes
// the text it applies to. Corrections apply only when a run asks for them
// (cua-bench run -corrected, on a set's [corrected] tasks); their results
// are kept and reported apart from the faithful upstream score.
type Correction struct {
	Why string
	// Pre and Grading replace Old with New in the pre_command and in every
	// grading command.
	Pre, Grading *Replace
}

// Replace is one literal replacement.
type Replace struct{ Old, New string }

// Corrections is keyed by task id.
var Corrections = map[string]Correction{
	// grep reads a pattern that starts with '-' as an option, prints its
	// usage and exits 2, so the grader never prints True. "--" ends the
	// options.
	"sys_apps/dbfb0512-cb02-984b-4d25-610b4f8f995a": {
		Why:     `grep -q "-7.000000" parses the pattern as an option; grep -q -- "-7.000000"`,
		Grading: &Replace{Old: `grep -q "-7.000000"`, New: `grep -q -- "-7.000000"`},
	},
	// The pre_command starts with a stray backtick, so zsh stops with
	// "unmatched `" and the starting state (previews shown always) is never
	// set.
	"sys_and_interface/17fed363-d017-b26c-22f2-ec1865306075": {
		Why: "the pre_command starts with a stray backtick; it is removed",
		Pre: &Replace{Old: "`osascript", New: "osascript"},
	},
}

// Correct applies the task's correction and records it on the task. It
// fails when the task has none or the text it replaces is gone.
func Correct(t *Task) error {
	c, ok := Corrections[t.ID]
	if !ok {
		return fmt.Errorf("%s: no correction", t.ID)
	}
	if c.Pre != nil {
		if !strings.Contains(t.Pre, c.Pre.Old) {
			return fmt.Errorf("%s: the pre_command no longer contains %q", t.ID, c.Pre.Old)
		}
		t.Pre = strings.Replace(t.Pre, c.Pre.Old, c.Pre.New, 1)
	}
	if c.Grading != nil {
		n := 0
		for i, g := range t.Grading {
			if strings.Contains(g.Command, c.Grading.Old) {
				t.Grading[i].Command = strings.ReplaceAll(g.Command, c.Grading.Old, c.Grading.New)
				n++
			}
		}
		if n == 0 {
			return fmt.Errorf("%s: no grading command contains %q", t.ID, c.Grading.Old)
		}
	}
	t.Corrected = c.Why
	return nil
}
