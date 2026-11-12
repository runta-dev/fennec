package bench

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	scoredFile  = regexp.MustCompile(`^trial-(\d+)\.json$`)
	harnessFile = regexp.MustCompile(`^trial-(\d+)-harness-(\d+)\.json$`)
)

// TrialBase is the file base name of a trial's scored result; harness
// failures are kept beside it under HarnessBase.
func TrialBase(trial int) string { return fmt.Sprintf("trial-%d", trial) }

// HarnessBase names one failed attempt of a trial.
func HarnessBase(trial, attempt int) string {
	return fmt.Sprintf("trial-%d-harness-%d", trial, attempt)
}

// TrialDir is where a (backend, task) pair's files go under a results root.
func TrialDir(root, backend, task string) string {
	return filepath.Join(root, backend, filepath.FromSlash(task))
}

// LoadResults reads every scored and harness-failure result under root.
func LoadResults(root string) (scored, harness []TrialResult, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		isScored, isHarness := scoredFile.MatchString(d.Name()), harnessFile.MatchString(d.Name())
		if !isScored && !isHarness {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var r TrialResult
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if isScored {
			scored = append(scored, r)
		} else {
			harness = append(harness, r)
		}
		return nil
	})
	return scored, harness, err
}

type agg struct {
	n, solved         int
	turns, tokens, ms int64
}

func (a *agg) add(r TrialResult) {
	a.n++
	if r.Score >= 100 {
		a.solved++
	}
	if r.Transcript != nil {
		a.turns += int64(r.Transcript.Turns)
		a.tokens += int64(r.Transcript.InputTokens + r.Transcript.OutputTokens)
	}
	a.ms += r.AgentMs
}

func (a agg) mean(x int64) float64 {
	if a.n == 0 {
		return 0
	}
	return float64(x) / float64(a.n)
}

// Summary renders the markdown report: per backend, per trial and per task.
func Summary(tasks []string, scored, harness []TrialResult) string {
	backends := []string{}
	seen := map[string]bool{}
	for _, b := range Backends {
		seen[b] = true
		backends = append(backends, b)
	}
	trials := map[int]bool{}
	byBackend := map[string]*agg{}
	byTrial := map[string]map[int]*agg{}
	byTask := map[string]map[string]*agg{}
	for _, r := range scored {
		if !seen[r.Backend] {
			seen[r.Backend] = true
			backends = append(backends, r.Backend)
		}
		trials[r.Trial] = true
		get := func(m map[string]*agg, k string) *agg {
			if m[k] == nil {
				m[k] = &agg{}
			}
			return m[k]
		}
		get(byBackend, r.Backend).add(r)
		if byTrial[r.Backend] == nil {
			byTrial[r.Backend] = map[int]*agg{}
		}
		if byTrial[r.Backend][r.Trial] == nil {
			byTrial[r.Backend][r.Trial] = &agg{}
		}
		byTrial[r.Backend][r.Trial].add(r)
		if byTask[r.Task] == nil {
			byTask[r.Task] = map[string]*agg{}
		}
		get(byTask[r.Task], r.Backend).add(r)
	}
	trialList := make([]int, 0, len(trials))
	for t := range trials {
		trialList = append(trialList, t)
	}
	sort.Ints(trialList)

	var b strings.Builder
	fmt.Fprintf(&b, "# macOSWorld subset: %d tasks\n\n", len(tasks))
	b.WriteString("## Per backend\n\n")
	b.WriteString("| Backend | Solved / scored | Rate |")
	for _, t := range trialList {
		fmt.Fprintf(&b, " Trial %d |", t)
	}
	b.WriteString(" Mean turns | Mean tokens | Mean agent time | Harness failures |\n|---|---|---|")
	for range trialList {
		b.WriteString("---|")
	}
	b.WriteString("---|---|---|---|\n")
	hf := map[string]int{}
	for _, r := range harness {
		hf[r.Backend]++
	}
	for _, be := range backends {
		a := byBackend[be]
		if a == nil {
			a = &agg{}
		}
		rate := 0.0
		if a.n > 0 {
			rate = 100 * float64(a.solved) / float64(a.n)
		}
		fmt.Fprintf(&b, "| %s | %d / %d | %.1f%% |", be, a.solved, a.n, rate)
		for _, t := range trialList {
			ta := byTrial[be][t]
			if ta == nil {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(&b, " %d / %d |", ta.solved, ta.n)
		}
		fmt.Fprintf(&b, " %.1f | %.0f | %s | %d |\n", a.mean(a.turns), a.mean(a.tokens), dur(a.mean(a.ms)), hf[be])
	}

	b.WriteString("\n## Per task\n\nEach cell: solved / scored trials, mean turns, mean agent time.\n\n| Task |")
	for _, be := range backends {
		fmt.Fprintf(&b, " %s |", be)
	}
	b.WriteString("\n|---|")
	for range backends {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "| %s |", t)
		for _, be := range backends {
			a := byTask[t][be]
			if a == nil {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(&b, " %d/%d, %.0f t, %s |", a.solved, a.n, a.mean(a.turns), dur(a.mean(a.ms)))
		}
		b.WriteString("\n")
	}

	if len(harness) > 0 {
		b.WriteString("\n## Harness failures (retried, not scored)\n\n| Backend | Task | Trial | Attempt | Error |\n|---|---|---|---|---|\n")
		sort.Slice(harness, func(i, j int) bool {
			if harness[i].Backend != harness[j].Backend {
				return harness[i].Backend < harness[j].Backend
			}
			if harness[i].Task != harness[j].Task {
				return harness[i].Task < harness[j].Task
			}
			return harness[i].Trial*100+harness[i].Attempt < harness[j].Trial*100+harness[j].Attempt
		})
		for _, r := range harness {
			msg := strings.ReplaceAll(clip(r.HarnessError, 160), "|", "/")
			msg = strings.ReplaceAll(msg, "\n", " ")
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %s |\n", r.Backend, r.Task, r.Trial, r.Attempt, msg)
		}
	}
	return b.String()
}

func dur(ms float64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}
