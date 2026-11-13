package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Board is the data the results page draws: every task, every scored run
// and every harness failure, without task texts or grading commands (the
// macOSWorld license keeps those out of anything shared).
type Board struct {
	Generated time.Time   `json:"generated"`
	Model     string      `json:"model,omitempty"`
	Trials    int         `json:"trials"`
	Backends  []string    `json:"backends"`
	Tasks     []BoardTask `json:"tasks"`
	Runs      []BoardRun  `json:"runs"`
	Harness   []BoardRun  `json:"harness_failures"`
	// Details says whether /run/... serves each run's transcript.
	Details bool `json:"details"`
}

// BoardTask is one selected task.
type BoardTask struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	App      string `json:"app,omitempty"`
}

// BoardRun is one trial, reduced to what the page shows.
type BoardRun struct {
	Task         string         `json:"task"`
	Backend      string         `json:"backend"`
	Trial        int            `json:"trial"`
	Attempt      int            `json:"attempt,omitempty"`
	Score        int            `json:"score"`
	EvalError    bool           `json:"eval_error,omitempty"`
	HarnessError string         `json:"harness_error,omitempty"`
	Stop         string         `json:"stop,omitempty"`
	Turns        int            `json:"turns"`
	ToolCalls    int            `json:"tool_calls"`
	ToolErrors   int            `json:"tool_errors"`
	Tools        map[string]int `json:"tools,omitempty"` // calls per tool
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	BootMs       int64          `json:"boot_ms"`
	AgentMs      int64          `json:"agent_ms"`
	TotalMs      int64          `json:"total_ms"`
	Started      time.Time      `json:"started"`
}

// NewBoard builds the page data from an id list and loaded results.
func NewBoard(list IDList, scored, harness []TrialResult, trials int, model string) Board {
	b := Board{Generated: time.Now().UTC(), Model: model, Trials: trials, Backends: Backends, Runs: []BoardRun{}, Harness: []BoardRun{}}
	known := map[string]bool{}
	for _, id := range list.IDs {
		cat, _, _ := strings.Cut(id, "/")
		b.Tasks = append(b.Tasks, BoardTask{ID: id, Category: cat, App: list.Apps[id]})
		known[id] = true
	}
	for _, r := range scored {
		if known[r.Task] {
			b.Runs = append(b.Runs, boardRun(r))
		}
		if r.Trial > b.Trials {
			b.Trials = r.Trial
		}
	}
	for _, r := range harness {
		if known[r.Task] {
			b.Harness = append(b.Harness, boardRun(r))
		}
	}
	byStart := func(rs []BoardRun) {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Started.Before(rs[j].Started) })
	}
	byStart(b.Runs)
	byStart(b.Harness)
	return b
}

func boardRun(r TrialResult) BoardRun {
	o := BoardRun{
		Task: r.Task, Backend: r.Backend, Trial: r.Trial, Attempt: r.Attempt, Score: r.Score,
		EvalError: r.EvalError, HarnessError: r.HarnessError,
		BootMs: r.BootMs, AgentMs: r.AgentMs, TotalMs: r.TotalMs, Started: r.Started,
	}
	if t := r.Transcript; t != nil {
		o.Stop, o.Turns, o.ToolCalls = t.Stop, t.Turns, t.ToolCalls
		o.InputTokens, o.OutputTokens = t.InputTokens, t.OutputTokens
		for _, s := range t.Steps {
			if s.Tool == "" {
				continue
			}
			if o.Tools == nil {
				o.Tools = map[string]int{}
			}
			o.Tools[s.Tool]++
			if s.IsError {
				o.ToolErrors++
			}
		}
	}
	return o
}

// RunDetail is one run's transcript as the page's run view shows it. It is
// served only from a local results directory, never exported.
type RunDetail struct {
	BoardRun
	Final      string        `json:"final,omitempty"`
	Steps      []Step        `json:"steps"`
	Grading    []GradeResult `json:"grading,omitempty"`
	Screenshot string        `json:"screenshot,omitempty"` // path under /shot/
}

// GradeResult is a grading command's outcome without the command itself.
type GradeResult struct {
	Score  int    `json:"score"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
}

// LoadRunDetail reads one scored run (trial) of backend on task.
func LoadRunDetail(root, backend, task string, trial int) (RunDetail, error) {
	if !validName(backend) || !validTask(task) || trial < 1 {
		return RunDetail{}, fmt.Errorf("bad run %s %s %d", backend, task, trial)
	}
	dir := TrialDir(root, backend, task)
	data, err := os.ReadFile(filepath.Join(dir, TrialBase(trial)+".json"))
	if err != nil {
		return RunDetail{}, err
	}
	var r TrialResult
	if err := json.Unmarshal(data, &r); err != nil {
		return RunDetail{}, err
	}
	d := RunDetail{BoardRun: boardRun(r), Steps: []Step{}}
	if t := r.Transcript; t != nil {
		d.Final = t.Final
		for _, s := range t.Steps {
			s.Result = clip(s.Result, 1500)
			d.Steps = append(d.Steps, s)
		}
	}
	for _, g := range r.Grading {
		d.Grading = append(d.Grading, GradeResult{Score: g.Score, OK: g.OK, Output: clip(g.Output, 300)})
	}
	if r.Screenshot != "" && validName(strings.TrimSuffix(r.Screenshot, ".jpg")) {
		d.Screenshot = path.Join(backend, task, r.Screenshot)
	}
	return d, nil
}

// ShotPath resolves a /shot/ path under root, refusing anything that is not
// backend/category/uuid/trial-N.jpg.
func ShotPath(root, rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	if len(parts) != 4 || !validName(parts[0]) || !validTask(parts[1]+"/"+parts[2]) || !scoredShot(parts[3]) {
		return "", false
	}
	return filepath.Join(root, filepath.FromSlash(rel)), true
}

func scoredShot(name string) bool {
	n, ok := strings.CutSuffix(name, ".jpg")
	return ok && scoredFile.MatchString(n+".json")
}

func validTask(task string) bool {
	cat, id, ok := strings.Cut(task, "/")
	return ok && validName(cat) && validName(id)
}

// validName is a path element made of letters, digits, '-' and '_' only.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
