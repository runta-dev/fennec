package bench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/bench/browserset"
	"fennec/cua/internal/vm"
)

// The browser set (bench/sets/browser.md) is our own: its tasks live in
// bench/browser/tasks.json, not in a macOSWorld checkout, and their ids in
// set files are "browser/ID". Each trial pushes bench-pages (the page
// server, setup, snapshot and grader, cmd/bench-pages) into the clone;
// the golden image only needs Chromium at browserset.GuestChromium.

// BrowserPrefix starts every browser task id.
const BrowserPrefix = "browser/"

// GuestPages is where each trial puts bench-pages.
const GuestPages = GuestBin + "/bench-pages"

// IsBrowserTask reports whether an id names a browser-set task.
func IsBrowserTask(id string) bool { return strings.HasPrefix(id, BrowserPrefix) }

// BrowserBackendEnv points fennec-cua's browser surface at the image's
// Chromium and at Chromium's default profile, the one the setup and a
// normally launched Chromium use, so all three backends work in the same
// browser and profile. The other backends ignore it.
const BrowserBackendEnv = `export FENNEC_CUA_CHROMIUM=` + browserset.GuestChromium + ` FENNEC_CUA_CHROMIUM_PROFILE="$HOME/Library/Application Support/Chromium"; `

// browserAutomation is what the browser set's commands script.
var browserAutomation = []string{"org.chromium.Chromium", "com.apple.Notes", "com.apple.TextEdit", "com.apple.systemevents"}

// LoadBrowserTask builds the runner's task for a browser-set id.
func LoadBrowserTask(id string) (Task, error) {
	d, err := browserset.Find(strings.TrimPrefix(id, BrowserPrefix))
	if err != nil {
		return Task{}, err
	}
	run := func(sub string) string { return GuestPages + " " + sub + " " + d.ID }
	return Task{
		ID:                 id,
		Text:               d.Text(browserset.DefaultBase),
		Pre:                run("setup"),
		ForceErrorFreePrep: true,
		BeforeGrading:      1,
		Grading:            []GradingCommand{{Command: run("grade"), Score: 100}},
		Install:            installPages,
		Automation:         browserAutomation,
		AfterAgent:         GuestPages + " snapshot",
		Solve:              run("solve"),
		BackendEnv:         BrowserBackendEnv,
	}, nil
}

// BrowserApps names a browser task's apps for set files and reports.
func BrowserApps(id string) string {
	d, err := browserset.Find(strings.TrimPrefix(id, BrowserPrefix))
	if err != nil {
		return ""
	}
	return strings.Join(d.Apps, ", ")
}

var pagesBuild struct {
	once  sync.Once
	stage string
	err   error
}

// buildPages cross-builds bench-pages for the VM (darwin/arm64, no cgo)
// once per process, into a staging folder holding bin/bench-pages.
func buildPages(ctx context.Context) (string, error) {
	pagesBuild.once.Do(func() {
		stage, err := os.MkdirTemp("", "cua-bench-pages")
		if err != nil {
			pagesBuild.err = err
			return
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(stage, "bin", "bench-pages"), "./cmd/bench-pages")
		cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			pagesBuild.err = fmt.Errorf("build bench-pages: %v: %s", err, out)
			return
		}
		pagesBuild.stage = stage
	})
	return pagesBuild.stage, pagesBuild.err
}

// installPages pushes bench-pages into the VM.
func installPages(ctx context.Context, s *vm.Session) error {
	stage, err := buildPages(ctx)
	if err != nil {
		return err
	}
	if err := s.Push(ctx, HomeDir+"/bench", "-C", stage, "bin/bench-pages"); err != nil {
		return err
	}
	_, err = s.Output(ctx, "test -x "+vm.Quote(GuestPages)+" && test -d "+vm.Quote(browserset.GuestChromium)+
		" || { echo 'the image has no "+browserset.GuestChromium+" (see bench/sets/browser.md)' >&2; exit 1; }")
	return err
}

// GuestRun runs a command in the VM's desktop session with zsh, as task
// commands run, and returns its combined output and whether it exited 0.
func GuestRun(ctx context.Context, s *vm.Session, command string, timeout time.Duration) (string, bool) {
	return guestShell(ctx, s, command, timeout)
}

// LoadAnyTask loads a task by id: a browser-set task from tasks.json, any
// other from the macOSWorld checkout mw (which may then not be empty).
func LoadAnyTask(mw, id string) (Task, error) {
	if IsBrowserTask(id) {
		return LoadBrowserTask(id)
	}
	if mw == "" {
		return Task{}, fmt.Errorf("%s is a macOSWorld task: give -macosworld", id)
	}
	return LoadTask(mw, id, "en")
}
