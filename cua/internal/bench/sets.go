package bench

import (
	"bufio"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Evaluation sets (bench/sets/README.md). Each has its own id file, runs
// and reports on its own, and never shares a task with another.
const (
	// SetDev: the tasks we look at while developing; transcripts welcome.
	SetDev = "dev"
	// SetHoldout: the held-out test set; sealed, so no transcripts.
	SetHoldout = "holdout"
	// SetSafety: macOSWorld's safety tasks, scored by their in-process
	// distraction as well as by their grader.
	SetSafety = "safety"
	// SetBrowser: our own browser tasks on local pages (browser.go).
	SetBrowser = "browser"
)

// SetNames lists the sets in report order.
var SetNames = []string{SetDev, SetHoldout, SetSafety, SetBrowser}

// SetPath is a set's id file under dir (bench/sets).
func SetPath(dir, name string) string { return filepath.Join(dir, name+".txt") }

// ReadSets reads every set file under dir that exists, in SetNames order.
func ReadSets(dir string) ([]IDList, error) {
	var out []IDList
	for _, name := range SetNames {
		l, err := ReadIDList(SetPath(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if l.Name == "" {
			l.Name = name
		}
		out = append(out, l)
	}
	return out, nil
}

// Feasibility classes (bench/sets/README.md).
const (
	ClassAsIs       = "asis"       // runs in the golden image unchanged
	ClassSetup      = "setup"      // runs after a setup step recreates snapshot state
	ClassRebuild    = "rebuild"    // runs on synthetic snapshot files
	ClassInternet   = "internet"   // needs the live internet
	ClassApps       = "apps"       // needs apps the image lacks (iWork, Xcode, iMovie)
	ClassAssets     = "assets"     // needs unpublished snapshot files whose content matters
	ClassInfeasible = "infeasible" // cannot be run faithfully here for another reason
	ClassBroken     = "broken"     // the upstream grader or pre_command is broken
)

// Feasibility is one task's line in bench/sets/feasibility.tsv.
type Feasibility struct {
	ID, Class, Reason, App string
}

// Category is the task's macOSWorld category.
func (f Feasibility) Category() string { c, _, _ := strings.Cut(f.ID, "/"); return c }

// Runnable reports whether the class can run in our image.
func (f Feasibility) Runnable() bool {
	return f.Class == ClassAsIs || f.Class == ClassSetup || f.Class == ClassRebuild
}

// SetupStep is the setup step the task needs before its pre_command, or "".
func (f Feasibility) SetupStep() string {
	if f.Class == ClassSetup || f.Class == ClassRebuild {
		return f.Reason
	}
	return ""
}

// FeasibilityTable is the whole table, in file order.
type FeasibilityTable struct {
	Commit string
	Rows   []Feasibility
	ByID   map[string]Feasibility
}

// ReadFeasibility reads feasibility.tsv: "id class reason app" separated
// by tabs, "#" comments, and a "# commit: SHA" line.
func ReadFeasibility(path string) (FeasibilityTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return FeasibilityTable{}, err
	}
	defer f.Close()
	t := FeasibilityTable{ByID: map[string]Feasibility{}}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if c, ok := strings.CutPrefix(line, "# commit:"); ok {
			t.Commit = strings.TrimSpace(c)
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 4 {
			return FeasibilityTable{}, fmt.Errorf("%s:%d: want 4 tab-separated columns, got %d", path, n, len(cols))
		}
		r := Feasibility{ID: cols[0], Class: cols[1], Reason: cols[2], App: cols[3]}
		if _, dup := t.ByID[r.ID]; dup {
			return FeasibilityTable{}, fmt.Errorf("%s:%d: %s listed twice", path, n, r.ID)
		}
		t.Rows = append(t.Rows, r)
		t.ByID[r.ID] = r
	}
	return t, sc.Err()
}

// Quota is how many held-out tasks to draw from a category; All takes
// every eligible task.
type Quota struct {
	N   int
	All bool
}

// ParseQuotas reads "advanced=all,sys_apps=10".
func ParseQuotas(s string) (map[string]Quota, error) {
	q := map[string]Quota{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cat, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("quota %q: want category=N or category=all", part)
		}
		if v == "all" {
			q[cat] = Quota{All: true}
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("quota %q: %v", part, err)
		}
		q[cat] = Quota{N: n}
	}
	return q, nil
}

// SelectHeldOut draws the held-out set: from each category with a quota,
// the tasks whose class is in classes and which no excluded set holds,
// sorted by id, shuffled by a generator seeded with seed and the category
// name, and cut to the quota. The result is sorted by category (in quota
// order as given by cats) and id, so the same inputs always give the same
// file.
func SelectHeldOut(t FeasibilityTable, exclude map[string]bool, classes map[string]bool, cats []string, quotas map[string]Quota, seed uint64) ([]Feasibility, error) {
	var out []Feasibility
	for _, cat := range cats {
		q, ok := quotas[cat]
		if !ok {
			continue
		}
		var pool []Feasibility
		for _, r := range t.Rows {
			if r.Category() == cat && classes[r.Class] && !exclude[r.ID] {
				pool = append(pool, r)
			}
		}
		sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
		n := q.N
		if q.All || n > len(pool) {
			if !q.All {
				return nil, fmt.Errorf("%s: quota %d, but only %d eligible tasks", cat, q.N, len(pool))
			}
			n = len(pool)
		}
		h := fnv.New64a()
		h.Write([]byte(cat))
		r := rand.New(rand.NewPCG(seed, h.Sum64()))
		r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		picked := pool[:n]
		sort.Slice(picked, func(i, j int) bool { return picked[i].ID < picked[j].ID })
		out = append(out, picked...)
	}
	return out, nil
}
