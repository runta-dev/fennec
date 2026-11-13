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
	return t, nil
}

func localize(cmd string) string { return strings.ReplaceAll(cmd, UpstreamHome, HomeDir) }

// IDList is a committed task selection: task ids and the upstream commit.
type IDList struct {
	Commit string
	IDs    []string
	Apps   map[string]string // id -> the app the task is about
}

// ReadIDList reads a selection file: one "category/uuid" per line, blank
// lines and "#" comments ignored, and a "# commit: SHA" line naming the
// upstream commit the ids come from. A trailing "# Name" on an id line
// names the task's app.
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
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, app, _ := strings.Cut(line, "#")
		id, app = strings.TrimSpace(id), strings.TrimSpace(app)
		l.IDs = append(l.IDs, id)
		if app != "" {
			if l.Apps == nil {
				l.Apps = map[string]string{}
			}
			l.Apps[id] = app
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
