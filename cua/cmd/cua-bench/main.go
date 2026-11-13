// Command cua-bench runs macOSWorld tasks against computer-use backends (raw
// cua-driver, shuttle and fennec-cua), each trial in a fresh tart clone of a
// golden macOS image, all driven by the same LLM agent loop on the host.
//
//	go run ./cmd/cua-bench golden [flags]   build the golden image
//	go run ./cmd/cua-bench run [flags]      run trials, then write summary.md
//	go run ./cmd/cua-bench summary [flags]  rewrite summary.md from results
//	go run ./cmd/cua-bench web [flags]      serve the live results page
//	go run ./cmd/cua-bench export [flags]   write the page and data.json to a folder
//
// Task texts and grading commands come from a local macOSWorld checkout
// (-macosworld); this repository holds only the ids (bench/macosworld.txt).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fennec/cua/internal/bench"
	"fennec/cua/internal/vm"
)

func main() {
	log.SetFlags(log.Ltime)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cua-bench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cua-bench golden | run | summary | web | export [flags]")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "golden":
		return golden(ctx, args[1:])
	case "run":
		return runBench(ctx, args[1:])
	case "summary":
		fs := flag.NewFlagSet("summary", flag.ExitOnError)
		results := fs.String("results", "bench-results", "results directory")
		ids := fs.String("ids", "bench/macosworld.txt", "task id list")
		_ = fs.Parse(args[1:])
		l, err := bench.ReadIDList(*ids)
		if err != nil {
			return err
		}
		return writeSummary(*results, l.IDs)
	case "web":
		return serveWeb(ctx, args[1:])
	case "export":
		return exportWeb(args[1:])
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func tartBin() string {
	if p, err := exec.LookPath("tart"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "tart")
}

// The apps the selected tasks use; the golden image opens each once so no
// first-launch screen greets an agent. Keys are the key codes that get past
// them on macOS 26: Return (36) accepts a welcome screen, Space (49) presses
// the focused "Not Now" or "Cancel" of the iCloud prompt that follows. An app
// left with a prompt open refuses to quit and would lose these settings.
var taskApps = []struct {
	Name string
	Keys []int
}{
	{"Calculator", nil}, {"Contacts", nil}, {"Reminders", []int{36, 49}}, {"Chess", nil}, {"Dictionary", nil},
	{"Activity Monitor", nil}, {"Freeform", []int{36, 36, 49}}, {"Notes", []int{36, 49}}, {"System Settings", nil},
}

// Apps grading commands script, by bundle id: the guest agent (the process
// TCC holds responsible for every tart exec command) and osascript may send
// them Apple events without a prompt.
var scriptedApps = []string{
	"com.apple.systemevents", "com.apple.AddressBook", "com.apple.reminders", "com.apple.Notes",
	"com.apple.systempreferences", "com.apple.Chess", "com.apple.calculator", "com.apple.freeform",
	"com.apple.Dictionary", "com.apple.ActivityMonitor", "com.apple.finder", "com.apple.dock",
}

func golden(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("golden", flag.ExitOnError)
	name := fs.String("name", "cua-bench-golden", "golden VM name")
	base := fs.String("base", vm.DefaultImage, "image to clone")
	display := fs.String("display", "1920x1080px", "display size")
	driver := fs.String("driver", "", "cua-driver binary (v0.47.14-loomhand.1)")
	shuttle := fs.String("shuttle", "", "Shuttle.app bundle (v0.1.50)")
	refresh := fs.Bool("refresh-fennec", false, "only rebuild fennec-cua and replace it in the existing golden image")
	_ = fs.Parse(args)
	v := &vm.VM{Name: *name, User: vm.DefaultUser, Tart: tartBin()}
	if *refresh {
		return refreshFennec(ctx, v)
	}
	if *driver == "" || *shuttle == "" {
		return errors.New("golden needs -driver and -shuttle")
	}
	exists, err := v.Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		log.Printf("cloning %s into %s", *base, *name)
		if err := v.Clone(ctx, *base); err != nil {
			return err
		}
	}
	if err := v.SetDisplay(ctx, *display); err != nil {
		return err
	}
	if _, err := v.Start(ctx); err != nil {
		return err
	}
	s, err := v.Connect(ctx)
	if err != nil {
		return err
	}

	// Backends.
	stage, err := os.MkdirTemp("", "cua-bench-golden")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	binDir := filepath.Join(stage, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := buildFennec(ctx, binDir); err != nil {
		return err
	}
	if out, err := exec.Command("cp", *driver, filepath.Join(binDir, "cua-driver")).CombinedOutput(); err != nil {
		return fmt.Errorf("copy cua-driver: %v: %s", err, out)
	}
	log.Printf("installing the backends")
	if err := s.Push(ctx, bench.HomeDir+"/bench", "-C", stage, "bin"); err != nil {
		return err
	}
	if _, err := s.Output(ctx, "rm -rf "+vm.Quote(bench.GuestShuttle)); err != nil {
		return err
	}
	if err := s.Push(ctx, filepath.Dir(bench.GuestShuttle), "-C", filepath.Dir(*shuttle), filepath.Base(*shuttle)); err != nil {
		return err
	}
	if base := filepath.Base(*shuttle); base != "Shuttle.app" {
		if _, err := s.Output(ctx, "mv "+vm.Quote(filepath.Dir(bench.GuestShuttle)+"/"+base)+" "+vm.Quote(bench.GuestShuttle)); err != nil {
			return err
		}
	}

	// Permissions: Accessibility and Screen Recording for every backend
	// binary and the shuttle bundle; Automation and app data for grading.
	log.Printf("granting permissions")
	for _, c := range []string{bench.GuestFennec, bench.GuestDriver, bench.ShuttleID, bench.GuestShuttle + "/Contents/MacOS/shuttle", bench.GuestShuttle + "/Contents/MacOS/cua-driver"} {
		if err := s.GrantTCC(ctx, c); err != nil {
			return fmt.Errorf("grant %s: %w", c, err)
		}
	}
	agent, err := s.Output(ctx, "readlink -f \"$(command -v tart-guest-agent)\"")
	if err != nil {
		return err
	}
	for _, client := range []string{strings.TrimSpace(agent), "/usr/bin/osascript"} {
		for _, app := range scriptedApps {
			if err := s.GrantUserTCC(ctx, "kTCCServiceAppleEvents", client, app); err != nil {
				return err
			}
		}
		for _, svc := range []string{"kTCCServiceAddressBook", "kTCCServiceReminders", "kTCCServiceCalendar"} {
			if err := s.GrantUserTCC(ctx, svc, client, ""); err != nil {
				return err
			}
		}
	}
	if err := s.ApproveScreenCapture(ctx, strings.TrimSpace(agent), "/usr/sbin/screencapture", bench.GuestFennec, bench.GuestDriver, bench.ShuttleID,
		bench.GuestShuttle+"/Contents/MacOS/shuttle", bench.GuestShuttle+"/Contents/MacOS/cua-driver"); err != nil {
		return err
	}

	// A quiet, default desktop: no restored windows, no update prompts, each
	// task app launched once past its welcome screens, then quit.
	log.Printf("preparing the desktop")
	prep := []string{
		"defaults write -g NSQuitAlwaysKeepsWindows -bool false",
		"defaults write com.apple.loginwindow TALLogoutSavesState -bool false",
		"sudo softwareupdate --schedule off >/dev/null 2>&1 || true",
		"osascript -e 'tell application \"Terminal\" to quit' || true",
	}
	for _, c := range prep {
		if _, err := s.Output(ctx, c); err != nil {
			return err
		}
	}
	for _, app := range taskApps {
		script := fmt.Sprintf("open -a %s; sleep 12", vm.Quote(app.Name))
		for _, k := range app.Keys {
			script += fmt.Sprintf(`; osascript -e 'tell application %q to activate' -e 'delay 2' -e 'tell application "System Events" to key code %d'; sleep 4`, app.Name, k)
		}
		if _, err := s.Output(ctx, script); err != nil {
			log.Printf("prime %s: %v", app.Name, err)
		}
		if _, err := s.Output(ctx, fmt.Sprintf(`osascript -e 'tell application %q to quit'; sleep 4`, app.Name)); err != nil {
			return fmt.Errorf("%s did not quit, so its first-launch state would be lost: %w", app.Name, err)
		}
	}
	if err := quietDesktop(ctx, s); err != nil {
		return err
	}
	log.Printf("shutting %s down", *name)
	return v.Shutdown(ctx)
}

type job struct {
	task    bench.Task
	backend string
	trial   int
}

func runBench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	mw := fs.String("macosworld", "", "macOSWorld checkout (showlab/macosworld)")
	ids := fs.String("ids", "bench/macosworld.txt", "task id list")
	only := fs.String("tasks", "", "comma-separated task ids or uuid prefixes to run (default all)")
	limit := fs.Int("limit", 0, "run only the first N tasks")
	backends := fs.String("backends", strings.Join(bench.Backends, ","), "backends to run")
	trials := fs.Int("trials", 1, "trials per task and backend")
	concurrency := fs.Int("concurrency", 1, "VMs at once (Apple allows two running macOS VMs per host)")
	golden := fs.String("golden", "cua-bench-golden", "golden VM to clone")
	results := fs.String("results", "bench-results", "results directory")
	modelURL := fs.String("model-url", "http://127.0.0.1:3425/v1", "agent model: OpenAI-compatible base URL")
	modelName := fs.String("model", "zai/glm-5.3-flashx", "agent model id")
	relay := fs.String("relay", "192.168.64.1:34250", "host address the VMs reach the model gateway on (empty: backends get no model)")
	maxTurns := fs.Int("max-turns", 40, "agent turns per trial")
	wall := fs.Duration("wall", 10*time.Minute, "agent wall clock per trial")
	retries := fs.Int("retries", 2, "retries of a trial after a harness failure")
	_ = fs.Parse(args)
	if *mw == "" {
		return errors.New("run needs -macosworld")
	}
	list, err := bench.ReadIDList(*ids)
	if err != nil {
		return err
	}
	if head, err := exec.Command("git", "-C", *mw, "rev-parse", "HEAD").Output(); err == nil && list.Commit != "" && strings.TrimSpace(string(head)) != list.Commit {
		log.Printf("warning: the checkout is at %s, the id list was made at %s", strings.TrimSpace(string(head)), list.Commit)
	}
	var tasks []bench.Task
	for _, id := range list.IDs {
		if *only != "" && !matches(id, *only) {
			continue
		}
		t, err := bench.LoadTask(*mw, id, "en")
		if err != nil {
			return err
		}
		tasks = append(tasks, t)
		if *limit > 0 && len(tasks) == *limit {
			break
		}
	}
	model := bench.Model{BaseURL: *modelURL, Name: *modelName}
	access := bench.ModelAccess{}
	if *relay != "" {
		addr, err := bench.Relay(ctx, *relay, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(*modelURL, "/v1"), "http://"), "https://"))
		if err != nil {
			return fmt.Errorf("model relay: %w", err)
		}
		access = bench.ModelAccess{BaseURL: "http://" + addr + "/v1", Model: *modelName}
		log.Printf("backends reach the model at %s", access.BaseURL)
	}

	var jobs []job
	for tr := 1; tr <= *trials; tr++ {
		for _, t := range tasks {
			for _, b := range strings.Split(*backends, ",") {
				b = strings.TrimSpace(b)
				dir := bench.TrialDir(*results, b, t.ID)
				if _, err := os.Stat(filepath.Join(dir, bench.TrialBase(tr)+".json")); err == nil {
					continue // already scored
				}
				jobs = append(jobs, job{task: t, backend: b, trial: tr})
			}
		}
	}
	log.Printf("%d trials to run, %d at a time", len(jobs), *concurrency)
	start := time.Now()
	queue := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				r := runJob(ctx, j, bench.TrialConfig{
					Task: j.task, Backend: j.backend, Trial: j.trial,
					Golden: *golden, Tart: tartBin(), Model: model, Access: access,
					MaxTurns: *maxTurns, WallClock: *wall,
					Dir: bench.TrialDir(*results, j.backend, j.task.ID),
				}, *retries)
				mu.Lock()
				done++
				log.Printf("[%d/%d] %s %s trial %d: %s", done, len(jobs), j.backend, j.task.ID, j.trial, r)
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		select {
		case queue <- j:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(queue)
	wg.Wait()
	log.Printf("finished in %s", time.Since(start).Round(time.Second))
	ids2 := make([]string, len(tasks))
	for i, t := range tasks {
		ids2[i] = t.ID
	}
	return writeSummary(*results, ids2)
}

func matches(id, filter string) bool {
	for _, f := range strings.Split(filter, ",") {
		f = strings.TrimSpace(f)
		if f != "" && (id == f || strings.HasPrefix(id[strings.Index(id, "/")+1:], f)) {
			return true
		}
	}
	return false
}

// runJob runs a trial, retrying harness failures, and returns a one-line
// outcome.
func runJob(ctx context.Context, j job, c bench.TrialConfig, retries int) string {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return "results: " + err.Error()
	}
	c.Log = log.Printf
	for attempt := 1; attempt <= retries+1; attempt++ {
		if ctx.Err() != nil {
			return "cancelled"
		}
		c.Base = bench.TrialBase(j.trial)
		r := bench.RunTrial(ctx, c, attempt)
		if r.HarnessError == "" {
			if err := bench.WriteResult(c.Dir, c.Base, r); err != nil {
				return "write: " + err.Error()
			}
			turns := 0
			if r.Transcript != nil {
				turns = r.Transcript.Turns
			}
			return fmt.Sprintf("score %d (%d turns, agent %s, total %s, stop %s)", r.Score, turns,
				(time.Duration(r.AgentMs) * time.Millisecond).Round(time.Second),
				(time.Duration(r.TotalMs) * time.Millisecond).Round(time.Second), stopOf(r))
		}
		// Keep the failed attempt's files under their own name.
		hb := bench.HarnessBase(j.trial, attempt)
		for _, ext := range []string{".log", ".jpg"} {
			_ = os.Rename(filepath.Join(c.Dir, c.Base+ext), filepath.Join(c.Dir, hb+ext))
		}
		_ = bench.WriteResult(c.Dir, hb, r)
		log.Printf("harness failure (%s %s trial %d attempt %d): %s", j.backend, j.task.ID, j.trial, attempt, r.HarnessError)
	}
	return "harness failure, retries exhausted"
}

func stopOf(r bench.TrialResult) string {
	if r.Transcript == nil {
		return "-"
	}
	return r.Transcript.Stop
}

func writeSummary(results string, ids []string) error {
	scored, harness, err := bench.LoadResults(results)
	if err != nil {
		return err
	}
	md := bench.Summary(ids, scored, harness)
	p := filepath.Join(results, "summary.md")
	if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Print(md)
	log.Printf("wrote %s", p)
	return nil
}

func buildFennec(ctx context.Context, dir string) error {
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(dir, "fennec-cua"), "./cmd/fennec-cua")
	build.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build fennec-cua: %w", err)
	}
	return nil
}

// refreshFennec replaces only the fennec-cua binary in an existing golden
// image, keeping the desktop state, the other backends and every grant (TCC
// grants and capture approvals are keyed by path, which does not change).
func refreshFennec(ctx context.Context, v *vm.VM) error {
	stage, err := os.MkdirTemp("", "cua-bench-golden")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	binDir := filepath.Join(stage, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := buildFennec(ctx, binDir); err != nil {
		return err
	}
	if _, err := v.Start(ctx); err != nil {
		return err
	}
	s, err := v.Connect(ctx)
	if err != nil {
		return err
	}
	log.Printf("replacing fennec-cua in %s", v.Name)
	if err := s.Push(ctx, bench.HomeDir+"/bench", "-C", stage, "bin/fennec-cua"); err != nil {
		return err
	}
	if err := s.GrantTCC(ctx, bench.GuestFennec); err != nil {
		return err
	}
	if out, err := s.Output(ctx, "shasum -a 256 "+vm.Quote(bench.GuestFennec)); err == nil {
		log.Printf("installed: %s", strings.TrimSpace(out))
	}
	if err := quietDesktop(ctx, s); err != nil {
		return err
	}
	log.Printf("shutting %s down", v.Name)
	return v.Shutdown(ctx)
}

// quietDesktop makes sure nothing comes back at the next login: loginwindow
// relaunches the apps of its last snapshot, which may predate the quits
// above. The Dock forgets the recently used apps. tart stop may end the VM
// before the page cache reaches the disk, hence the sync.
func quietDesktop(ctx context.Context, s *vm.Session) error {
	cleanup := []string{
		`plutil -replace PersistentApps -json "[]" "$HOME/Library/Group Containers/group.com.apple.loginwindow.persistent-apps/persistantApps" || true`,
		`defaults -currentHost write com.apple.loginwindow TALAppsToRelaunchAtLogin -array`,
		// Twice: a Dock that restarts right after an app quit may write
		// that app back.
		`defaults delete com.apple.dock recent-apps 2>/dev/null; killall Dock; sleep 5`,
		`defaults delete com.apple.dock recent-apps 2>/dev/null; killall Dock; sleep 5`,
		`sync; sleep 3`,
	}
	for _, c := range cleanup {
		if _, err := s.Output(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
