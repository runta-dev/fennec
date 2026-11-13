package bench

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Task is one macOSWorld task (showlab/macosworld). Task texts and grading
// commands are read from a local checkout at run time and never stored in
// this repository: the dataset is CC BY-NC 4.0, for internal evaluation only.
type Task struct {
	// ID is "category/uuid", the path of the task file under tasks/.
	ID   string
	Text string
	// Pre is the preparation command, empty when the task has none.
	Pre string
	// Delays in seconds, as the task file gives them.
	BeforeAction  int
	BeforeGrading int
	// ForceErrorFreePrep makes a failing preparation a harness failure.
	ForceErrorFreePrep bool
	Grading            []GradingCommand
	// InProcess is a safety task's distraction, nil for other tasks.
	InProcess *InProcess
	// Corrected names the harness corrections applied to the task
	// (corrections.go); empty for the task as upstream wrote it.
	Corrected string
}

// InProcess is macOSWorld's in-process event: a command, usually an
// osascript dialog, started while the agent works. Its outcome is graded by
// the button the agent pressed: one of Gold handled the distraction, one of
// Distracting fell for it.
type InProcess struct {
	Command     string
	Step        int
	Gold        []string
	Distracting []string
}

// GradingCommand is one [shell command, score] pair.
type GradingCommand struct {
	Command string
	Score   int
}

// taskFile is the subset of a macOSWorld task file the runner uses.
type taskFile struct {
	ID                 string            `json:"id"`
	Task               map[string]string `json:"task"`
	PreCommand         json.RawMessage   `json:"pre_command"`
	BeforeAction       *int              `json:"before_action_delay_seconds"`
	BeforeGrading      *int              `json:"before_grading_delay_seconds"`
	ForceErrorFreePrep bool              `json:"force_error_free_prep"`
	RawGrading         json.RawMessage   `json:"grading_command"`
	InProcess          json.RawMessage   `json:"in_process"`
}

// UpstreamHome is the guest user's home in the upstream environment; the
// runner's VMs use HomeDir instead.
const (
	UpstreamHome = "/Users/ec2-user"
	HomeDir      = "/Users/admin"
)

// LoadTask reads tasks/<id>.json from a macOSWorld checkout in the given
// language ("en"). The environment language is the same as the task's.
func LoadTask(root, id, lang string) (Task, error) {
	data, err := os.ReadFile(filepath.Join(root, "tasks", id+".json"))
	if err != nil {
		return Task{}, err
	}
	var f taskFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Task{}, fmt.Errorf("%s: %w", id, err)
	}
	t := Task{ID: id, Text: f.Task[lang], ForceErrorFreePrep: f.ForceErrorFreePrep}
	if t.Text == "" {
		return Task{}, fmt.Errorf("%s: no task text in %q", id, lang)
	}
	if f.BeforeAction != nil {
		t.BeforeAction = *f.BeforeAction
	}
	if f.BeforeGrading != nil {
		t.BeforeGrading = *f.BeforeGrading
	}
	// pre_command is a string or a map from environment language to string.
	if len(f.PreCommand) > 0 && string(f.PreCommand) != "null" {
		var s string
		if err := json.Unmarshal(f.PreCommand, &s); err != nil {
			var m map[string]string
			if err := json.Unmarshal(f.PreCommand, &m); err != nil {
				return Task{}, fmt.Errorf("%s: pre_command is neither a string nor a map", id)
			}
			var ok bool
			if s, ok = m[lang]; !ok {
				return Task{}, fmt.Errorf("%s: no pre_command for %q", id, lang)
			}
		}
		t.Pre = localize(s)
	}
	var grading [][2]json.RawMessage
	if err := json.Unmarshal(f.RawGrading, &grading); err != nil {
		return Task{}, fmt.Errorf("%s: grading_command: %w", id, err)
	}
	for _, g := range grading {
		var gc GradingCommand
		if err := json.Unmarshal(g[0], &gc.Command); err != nil {
			return Task{}, fmt.Errorf("%s: grading command: %w", id, err)
		}
		if err := json.Unmarshal(g[1], &gc.Score); err != nil {
			return Task{}, fmt.Errorf("%s: grading score: %w", id, err)
		}
		gc.Command = localize(gc.Command)
		t.Grading = append(t.Grading, gc)
	}
	if len(t.Grading) == 0 {
		return Task{}, fmt.Errorf("%s: no grading command", id)
	}
	if len(f.InProcess) > 0 && string(f.InProcess) != "null" {
		// [command, start step, gold elements, distracting elements]
		var raw []json.RawMessage
		if err := json.Unmarshal(f.InProcess, &raw); err != nil || len(raw) != 4 {
			return Task{}, fmt.Errorf("%s: in_process is not [command, step, gold, distracting]", id)
		}
		var ip InProcess
		for i, dst := range []any{&ip.Command, &ip.Step, &ip.Gold, &ip.Distracting} {
			if err := json.Unmarshal(raw[i], dst); err != nil {
				return Task{}, fmt.Errorf("%s: in_process[%d]: %w", id, i, err)
			}
		}
		ip.Command = localize(ip.Command)
		t.InProcess = &ip
	}
	return t, nil
}

func localize(cmd string) string { return strings.ReplaceAll(cmd, UpstreamHome, HomeDir) }

// IDList is a committed task selection: task ids and the upstream commit.
type IDList struct {
	Commit string
	// Name is the evaluation set ("# set: NAME"), empty for a plain list.
	Name string
	// Sealed marks a held-out set ("# sealed"): its runs keep no
	// transcripts or screenshots and the page shows no per-task details.
	Sealed bool
	IDs    []string
	Apps   map[string]string   // id -> the app the task is about
	Tags   map[string][]string // id -> tags such as "smoke" or "corrected"
}

// Task tags a set file may carry.
const (
	// TagSmoke marks a task every backend solved in every trial: a quick
	// check that the harness works, not a measure of a backend.
	TagSmoke = "smoke"
	// TagCorrected marks a task with a broken upstream grader or
	// pre_command and a harness correction (corrections.go). Its corrected
	// runs are scored apart from the faithful upstream score.
	TagCorrected = "corrected"
	// TagSkip marks a task that is listed for completeness but not run;
	// "[skip: reason]" says why.
	TagSkip = "skip"
)

// HasTag reports whether a task carries a tag (the part before any ':').
func (l IDList) HasTag(id, tag string) bool {
	for _, t := range l.Tags[id] {
		if name, _, _ := strings.Cut(t, ":"); strings.TrimSpace(name) == tag {
			return true
		}
	}
	return false
}

// Runnable is the ids without the skip tag, in file order.
func (l IDList) Runnable() []string {
	var out []string
	for _, id := range l.IDs {
		if !l.HasTag(id, TagSkip) {
			out = append(out, id)
		}
	}
	return out
}

// ReadIDList reads a selection file: one "category/uuid" per line, blank
// lines and "#" comments ignored, a "# commit: SHA" line naming the upstream
// commit the ids come from, and for an evaluation set a "# set: NAME" line
// and an optional "# sealed" line. A trailing "# Name [tag] [tag: note]" on
// an id line names the task's app and its tags.
func ReadIDList(path string) (IDList, error) {
	f, err := os.Open(path)
	if err != nil {
		return IDList{}, err
	}
	defer f.Close()
	var l IDList
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if c, ok := strings.CutPrefix(line, "# commit:"); ok {
			l.Commit = strings.TrimSpace(c)
			continue
		}
		if c, ok := strings.CutPrefix(line, "# set:"); ok {
			l.Name = strings.TrimSpace(c)
			continue
		}
		if line == "# sealed" {
			l.Sealed = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, rest, _ := strings.Cut(line, "#")
		id = strings.TrimSpace(id)
		app, tags := splitTags(rest)
		l.IDs = append(l.IDs, id)
		if app != "" {
			if l.Apps == nil {
				l.Apps = map[string]string{}
			}
			l.Apps[id] = app
		}
		if len(tags) > 0 {
			if l.Tags == nil {
				l.Tags = map[string][]string{}
			}
			l.Tags[id] = tags
		}
	}
	if err := sc.Err(); err != nil {
		return IDList{}, err
	}
	if len(l.IDs) == 0 {
		return IDList{}, errors.New(path + ": no task ids")
	}
	return l, nil
}

// splitTags splits "Calculator [smoke] [skip: needs Pages]" into the app
// and its bracketed tags.
func splitTags(s string) (app string, tags []string) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "[")
	if i < 0 {
		return s, nil
	}
	app, s = strings.TrimSpace(s[:i]), s[i:]
	for {
		open := strings.Index(s, "[")
		if open < 0 {
			break
		}
		end := strings.Index(s[open:], "]")
		if end < 0 {
			break
		}
		if t := strings.TrimSpace(s[open+1 : open+end]); t != "" {
			tags = append(tags, t)
		}
		s = s[open+end+1:]
	}
	return app, tags
}

// GradeOutput is what one grading command printed (stdout and stderr
// together) and whether it exited 0.
type GradeOutput struct {
	Command string `json:"command"`
	Score   int    `json:"score"`
	OK      bool   `json:"ok"`
	Output  string `json:"output"`
}

// Grade applies macOSWorld's binary grading: only commands worth 100 count,
// in order; the first that exits 0 and prints "true" (any case, anywhere in
// its output) scores 100, one that prints anything else passes on to the
// next, and one that fails stops grading as an evaluation error. run runs a
// command and reports its combined output and success.
func Grade(cmds []GradingCommand, run func(cmd string) (string, bool)) (score int, outs []GradeOutput, evalErr bool) {
	for _, c := range cmds {
		if c.Score != 100 {
			continue
		}
		out, ok := run(c.Command)
		outs = append(outs, GradeOutput{Command: c.Command, Score: c.Score, OK: ok, Output: out})
		if !ok {
			return 0, outs, true
		}
		if strings.Contains(strings.ToLower(out), "true") {
			return c.Score, outs, false
		}
	}
	return 0, outs, false
}
