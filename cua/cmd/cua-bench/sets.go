package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/bench"
	"fennec/cua/internal/vm"
)

// setFlags pick the task list: an evaluation set from bench/sets (-set),
// or any id file (-ids).
type setFlags struct {
	set, dir, ids *string
}

func addSetFlags(fs *flag.FlagSet) setFlags {
	return setFlags{
		set: fs.String("set", bench.SetDev, "evaluation set: "+strings.Join(bench.SetNames, ", ")+" (bench/sets/NAME.txt)"),
		dir: fs.String("sets", "bench/sets", "directory of the set files"),
		ids: fs.String("ids", "", "task id file to use instead of a set"),
	}
}

func (f setFlags) list() (bench.IDList, error) {
	if *f.ids != "" {
		return bench.ReadIDList(*f.ids)
	}
	l, err := bench.ReadIDList(bench.SetPath(*f.dir, *f.set))
	if err != nil {
		return l, err
	}
	if l.Name == "" {
		l.Name = *f.set
	}
	return l, nil
}

// selectHeldOut draws the held-out set from the feasibility table and
// writes it as a set file.
func selectHeldOut(args []string) error {
	fs := flag.NewFlagSet("select", flag.ExitOnError)
	dir := fs.String("sets", "bench/sets", "directory of the set files and feasibility.tsv")
	seed := fs.Uint64("seed", 20261113, "seed of the draw")
	quotas := fs.String("quotas", "advanced=all,multi_apps=all,file_management=all,media=all,sys_apps=10,productivity=10,sys_and_interface=10",
		"tasks per category, in output order: N or all")
	classes := fs.String("classes", "asis,setup,rebuild", "feasibility classes a held-out task may have")
	out := fs.String("out", "", "output file (default SETS/holdout.txt; - for stdout)")
	_ = fs.Parse(args)
	table, err := bench.ReadFeasibility(filepath.Join(*dir, "feasibility.tsv"))
	if err != nil {
		return err
	}
	q, err := bench.ParseQuotas(*quotas)
	if err != nil {
		return err
	}
	var cats []string
	for _, part := range strings.Split(*quotas, ",") {
		if c, _, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			cats = append(cats, c)
		}
	}
	allowed := map[string]bool{}
	for _, c := range strings.Split(*classes, ",") {
		allowed[strings.TrimSpace(c)] = true
	}
	// No task in two sets: the dev set's tasks and the safety category
	// (its own set) are excluded.
	exclude := map[string]bool{}
	for _, name := range []string{bench.SetDev, bench.SetSafety} {
		l, err := bench.ReadIDList(bench.SetPath(*dir, name))
		if err != nil {
			return fmt.Errorf("the %s set: %w", name, err)
		}
		for _, id := range l.IDs {
			exclude[id] = true
		}
	}
	picked, err := bench.SelectHeldOut(table, exclude, allowed, cats, q, *seed)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# set: holdout\n# sealed\n")
	b.WriteString("# The held-out test set. Sealed: cua-bench run keeps no transcripts or\n")
	b.WriteString("# screenshots of it and the results page shows only its rankings. Never\n")
	b.WriteString("# read its runs to tune a backend, prompt or harness (bench/sets/README.md).\n")
	fmt.Fprintf(&b, "# Drawn by: go run ./cmd/cua-bench select -seed %d -quotas %s -classes %s\n", *seed, *quotas, *classes)
	b.WriteString("# A trailing \"# App [setup: step]\" names the app and the setup step, if any.\n")
	fmt.Fprintf(&b, "# commit: %s\n", table.Commit)
	counts := map[string]int{}
	for _, r := range picked {
		counts[r.Category()]++
		tag := ""
		if s := r.SetupStep(); s != "" {
			tag = " [setup: " + s + "]"
		}
		fmt.Fprintf(&b, "%s  # %s%s\n", r.ID, r.App, tag)
	}
	if *out == "-" {
		fmt.Print(b.String())
	} else {
		p := *out
		if p == "" {
			p = bench.SetPath(*dir, bench.SetHoldout)
		}
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			return err
		}
		log.Printf("wrote %s", p)
	}
	for _, c := range cats {
		log.Printf("%-18s %d", c, counts[c])
	}
	log.Printf("%-18s %d", "total", len(picked))
	return nil
}

// checkTasks runs each task's preparation and grading in a fresh clone of
// the golden image without an agent: the setup step, the pre_command, and
// the grader on the untouched state, which should score 0. Several tasks
// share one clone with -per-vm; that is quicker and fine for this check,
// since every task's preparation resets what it uses.
func checkTasks(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	mw := fs.String("macosworld", "", "macOSWorld checkout (showlab/macosworld)")
	sel := addSetFlags(fs)
	feas := fs.String("feasibility", "bench/sets/feasibility.tsv", "feasibility table")
	only := fs.String("tasks", "", "comma-separated task ids or uuid prefixes (default the set's runnable tasks)")
	all := fs.Bool("all", false, "with -tasks: look the ids up in the feasibility table instead of the set")
	golden := fs.String("golden", "cua-bench-golden", "golden VM to clone")
	perVM := fs.Int("per-vm", 1, "tasks checked one after another in one clone")
	concurrency := fs.Int("concurrency", 1, "clones at once (at most 2 macOS VMs run per host)")
	corrected := fs.Bool("corrected", false, "apply the harness corrections to [corrected] tasks")
	keep := fs.Bool("keep", false, "leave the clones running for a look (tart stop and tart delete them afterwards)")
	_ = fs.Parse(args)
	if *mw == "" {
		return errors.New("check needs -macosworld")
	}
	table, err := bench.ReadFeasibility(*feas)
	if err != nil {
		return err
	}
	var ids []string
	var list bench.IDList
	if *all {
		for _, r := range table.Rows {
			if matches(r.ID, *only) {
				ids = append(ids, r.ID)
			}
		}
	} else {
		if list, err = sel.list(); err != nil {
			return err
		}
		for _, id := range list.Runnable() {
			if *only == "" || matches(id, *only) {
				ids = append(ids, id)
			}
		}
	}
	var tasks []bench.Task
	for _, id := range ids {
		t, err := bench.LoadTask(*mw, id, "en")
		if err != nil {
			return err
		}
		if *corrected && list.HasTag(id, bench.TagCorrected) {
			if err := bench.Correct(&t); err != nil {
				return err
			}
		}
		tasks = append(tasks, t)
	}
	if *perVM < 1 {
		*perVM = 1
	}
	var batches [][]bench.Task
	for i := 0; i < len(tasks); i += *perVM {
		batches = append(batches, tasks[i:min(i+*perVM, len(tasks))])
	}
	log.Printf("checking %d tasks in %d clones, %d at a time", len(tasks), len(batches), *concurrency)
	queue := make(chan []bench.Task)
	var mu sync.Mutex
	var lines []string
	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for batch := range queue {
				for _, l := range checkBatch(ctx, *golden, table, batch, w, *keep) {
					mu.Lock()
					lines = append(lines, l)
					log.Print(l)
					mu.Unlock()
				}
			}
		}(w)
	}
	for _, b := range batches {
		select {
		case queue <- b:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(queue)
	wg.Wait()
	sort.Strings(lines)
	fmt.Println("task\tsetup\tpre_ok\tscore\teval_error\tnote")
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// checkBatch checks tasks one after another in one fresh clone and returns
// one tab-separated line per task.
func checkBatch(ctx context.Context, golden string, table bench.FeasibilityTable, tasks []bench.Task, worker int, keep bool) []string {
	name := fmt.Sprintf("cua-bench-check-%d-%d", worker, time.Now().UnixNano()%1e9)
	v := &vm.VM{Name: name, User: vm.DefaultUser, Tart: tartBin()}
	fail := func(msg string) []string {
		var out []string
		for _, t := range tasks {
			out = append(out, fmt.Sprintf("%s\t-\t-\t-\t-\tharness: %s", t.ID, msg))
		}
		return out
	}
	defer func() {
		if keep {
			log.Printf("kept %s running", name)
			return
		}
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = v.Stop(dctx)
		if err := v.Delete(dctx); err != nil {
			log.Printf("%s: delete: %v", name, err)
		}
	}()
	if err := v.Clone(ctx, golden); err != nil {
		return fail("clone: " + err.Error())
	}
	if _, err := v.Start(ctx); err != nil {
		return fail("boot: " + err.Error())
	}
	s, err := v.Connect(ctx)
	if err != nil {
		return fail("connect: " + err.Error())
	}
	var out []string
	for i, t := range tasks {
		if i == 0 {
			if err := bench.WaitDesktop(ctx, s); err != nil {
				return fail("desktop: " + err.Error())
			}
		}
		setup := table.ByID[t.ID].SetupStep()
		pre, err := bench.Prepare(ctx, s, t, setup)
		preOK := "-"
		if pre.OK != nil {
			preOK = fmt.Sprint(*pre.OK)
		}
		note := ""
		if err != nil {
			note = "prepare: " + err.Error()
		} else if pre.OK != nil && !*pre.OK {
			note = "pre_command: " + pre.Output
		}
		if err == nil {
			time.Sleep(time.Duration(t.BeforeAction) * time.Second)
		}
		score, outs, evalErr := bench.GradeIn(ctx, s, t)
		if note == "" && len(outs) > 0 {
			note = "grader said: " + outs[len(outs)-1].Output
		}
		note = strings.Join(strings.Fields(note), " ")
		if len(note) > 300 {
			note = note[:300] + "..."
		}
		if setup == "" {
			setup = "-"
		}
		out = append(out, fmt.Sprintf("%s\t%s\t%s\t%d\t%v\t%s", t.ID, setup, preOK, score, evalErr, note))
	}
	return out
}
