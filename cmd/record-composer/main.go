// record-composer records the standard composer scenarios through fen on the
// Mac and replays them against the prediction profiles, so a profile that no
// longer matches an agent's new release shows up in minutes. It launches a
// labelled test session per recording (fen launch), drives it in a private
// tmux session with FEN_PREDICT_TRACE, and stops it. No scenario ever submits
// a prompt (see checkSafe), so no inference is used.
//
//	record-composer [-agents claude,codex,opencode,grok] [-scenarios typing,edit,menu,paste]
//	                [-size 100x30] [-baseline internal/predict/testdata/traces] [-save]
//	record-composer -replay <trace-dir> -agent <agent> [-size 100x30]
//
// Each recording is scanned for private text (home path, user and host names,
// other sessions' names, letters no scenario types, as from history or
// mention popups); only clean recordings are saved, with -save, into the
// baseline directory as <agent>-<scenario>-<cols>x<rows>.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"fennec/internal/launcher"
	"fennec/internal/predict"
)

const typingGap = 90 * time.Millisecond

type config struct {
	fen, tmux  string
	rows, cols int
	baseline   string
	save       bool
	verbose    bool
}

func main() {
	agents := flag.String("agents", "claude,codex,opencode,grok", "agents to record")
	names := flag.String("scenarios", "typing,edit,menu,paste", "scenarios to record")
	size := flag.String("size", "100x30", "terminal sizes, columns x rows, comma-separated (e.g. 80x24,100x30,160x45)")
	replay := flag.String("replay", "", "only replay this recorded trace directory")
	agent := flag.String("agent", "", "agent of the -replay trace")
	var cfg config
	flag.StringVar(&cfg.fen, "fen", "/Applications/Fennec.app/Contents/MacOS/fen", "fen executable")
	flag.StringVar(&cfg.tmux, "tmux", "", "tmux executable (default: from PATH or Homebrew)")
	flag.StringVar(&cfg.baseline, "baseline", "internal/predict/testdata/traces", "committed recordings to compare with")
	flag.BoolVar(&cfg.save, "save", false, "save clean recordings into the baseline directory")
	flag.BoolVar(&cfg.verbose, "v", false, "print every replay problem")
	flag.Parse()
	var sizes [][2]int
	for _, s := range split(*size) {
		cols, rows, err := parseSize(s)
		if err != nil {
			fail(err)
		}
		sizes = append(sizes, [2]int{cols, rows})
	}
	if len(sizes) == 0 {
		fail(errors.New("-size names no size"))
	}
	cfg.cols, cfg.rows = sizes[0][0], sizes[0][1]
	if *replay != "" {
		if predict.ProfileFor(*agent) == nil {
			fail(fmt.Errorf("-agent must name a profiled agent"))
		}
		r := run{agent: *agent, scenario: filepath.Base(*replay), dir: *replay, cols: cfg.cols, rows: cfg.rows}
		r.check(cfg)
		os.Exit(report([]run{r}, cfg))
	}
	if cfg.tmux == "" {
		cfg.tmux = findTmux()
	}
	cleanupStale(cfg)
	var runs []run
	for _, a := range split(*agents) {
		if !launcher.Supported[a] {
			fail(fmt.Errorf("fen does not launch agent %q", a))
		}
		for _, name := range split(*names) {
			build, ok := scenarios[name]
			if !ok {
				fail(fmt.Errorf("unknown scenario %q", name))
			}
			steps := build(a)
			if err := checkSafe(steps); err != nil {
				fail(fmt.Errorf("scenario %s: %w", name, err))
			}
			for _, sz := range sizes {
				c := cfg
				c.cols, c.rows = sz[0], sz[1]
				fmt.Fprintf(os.Stderr, "recording %s %s at %dx%d...\n", a, name, c.cols, c.rows)
				r := run{agent: a, scenario: name, cols: c.cols, rows: c.rows, typed: typedRunes(steps)}
				r.dir, r.session, r.err = record(c, a, steps)
				if r.err == nil {
					r.check(c)
					if c.save {
						r.saveTo(c)
					}
				}
				runs = append(runs, r)
			}
		}
	}
	os.Exit(report(runs, cfg))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "record-composer:", err)
	os.Exit(2)
}

func split(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func parseSize(s string) (int, int, error) {
	c, r, ok := strings.Cut(s, "x")
	cols, err1 := strconv.Atoi(c)
	rows, err2 := strconv.Atoi(r)
	if !ok || err1 != nil || err2 != nil || cols < 40 || rows < 12 {
		return 0, 0, fmt.Errorf("bad -size %q (want e.g. 100x30)", s)
	}
	return cols, rows, nil
}

func findTmux() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	fail(errors.New("tmux not found; install it or pass -tmux"))
	return ""
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// record launches a test session, drives it through tmux and returns the
// trace directory and the session ID.
func record(cfg config, agent string, steps []step) (string, string, error) {
	home, _ := os.UserHomeDir()
	work, err := os.MkdirTemp(home, "fennec-record-composer-")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(work) // only if still empty
	traces, err := os.MkdirTemp("", "fennec-record-composer-trace-")
	if err != nil {
		return "", "", err
	}
	launch := exec.Command(cfg.fen, "launch", agent)
	launch.Dir = work
	out, err := launch.Output()
	if err != nil {
		return "", "", fmt.Errorf("fen launch %s: %w", agent, err)
	}
	m := regexp.MustCompile(`"session_id"\s*:\s*"([^"]+)"`).FindSubmatch(out)
	if m == nil {
		return "", "", fmt.Errorf("fen launch %s returned no session ID", agent)
	}
	session := string(m[1])
	pane := "record-composer-" + strconv.Itoa(os.Getpid())
	tmux := func(args ...string) ([]byte, error) { return exec.Command(cfg.tmux, args...).Output() }
	var once sync.Once
	stop := func() {
		once.Do(func() {
			// Local cleanup first: fen stop may wait long when the cloud is
			// unreachable.
			tmux("send-keys", "-t", pane, "-H", "1c") // fen's detach key
			time.Sleep(1500 * time.Millisecond)
			tmux("kill-session", "-t", pane)
			os.Remove(work)
			stopSession(cfg, session)
		})
	}
	// Interrupted (or the SSH connection dropped): never leave the test
	// session or the tmux session behind.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	defer func() { signal.Stop(signals); close(done) }()
	go func() {
		select {
		case <-signals:
			stop()
			fmt.Fprintln(os.Stderr, "record-composer: interrupted; stopped test session", session)
			os.Exit(130)
		case <-done:
		}
	}()
	command := fmt.Sprintf("cd %s && FEN_PREDICT_TRACE=%s FEN_PREDICT_STATS=test %s attach %s; sleep 600", shellQuote(work), shellQuote(traces), shellQuote(cfg.fen), shellQuote(session))
	if _, err := tmux("new-session", "-d", "-s", pane, "-x", strconv.Itoa(cfg.cols), "-y", strconv.Itoa(cfg.rows), command); err != nil {
		stopSession(cfg, session)
		return "", session, fmt.Errorf("tmux: %w", err)
	}
	if err := waitReady(agent, func() string { s, _ := tmux("capture-pane", "-p", "-t", pane); return string(s) }, func(hex ...string) {
		tmux(append([]string{"send-keys", "-t", pane, "-H"}, hex...)...)
	}); err != nil {
		stop()
		return "", session, err
	}
	for _, s := range steps {
		switch {
		case s.commit != "":
			tmux("send-keys", "-t", pane, "-l", s.commit)
			time.Sleep(400 * time.Millisecond) // choosing the next candidate
		case s.typed != "":
			for _, r := range s.typed {
				tmux("send-keys", "-t", pane, "-l", string(r))
				time.Sleep(typingGap)
			}
		case s.keys != nil:
			tmux(append([]string{"send-keys", "-t", pane, "-H"}, hexBytes(s.keys)...)...)
			time.Sleep(350 * time.Millisecond)
		default:
			data := append(append([]byte("\x1b[200~"), s.paste...), "\x1b[201~"...)
			tmux(append([]string{"send-keys", "-t", pane, "-H"}, hexBytes(data)...)...)
			time.Sleep(time.Second)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	stop()
	entries, err := os.ReadDir(traces)
	if err != nil || len(entries) != 1 {
		return "", session, fmt.Errorf("expected one trace in %s", traces)
	}
	return filepath.Join(traces, entries[0].Name()), session, nil
}

// stopSession stops a test session, giving up after 30 s.
func stopSession(cfg config, session string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, cfg.fen, "stop", session).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "record-composer: fen stop %s: %v (stop it with: fen stop %s)\n", session, err, session)
	}
}

var (
	paneName      = regexp.MustCompile(`^record-composer-(\d+)$`)
	attachSession = regexp.MustCompile(`attach '([a-z0-9-]+)'`)
)

// cleanupStale stops what an earlier run left when it was killed: its tmux
// session (named after its process ID, no longer running), the test session
// it attached (from the pane's start command), and its empty work
// directories.
func cleanupStale(cfg config) {
	out, err := exec.Command(cfg.tmux, "list-sessions", "-F", "#{session_name}").Output()
	if err == nil {
		for _, name := range strings.Fields(string(out)) {
			m := paneName.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			if pid, _ := strconv.Atoi(m[1]); pid == os.Getpid() || syscall.Kill(pid, 0) == nil {
				continue // still running
			}
			start, _ := exec.Command(cfg.tmux, "display-message", "-p", "-t", name, "#{pane_start_command}").Output()
			exec.Command(cfg.tmux, "kill-session", "-t", name).Run()
			if s := attachSession.FindSubmatch(start); s != nil {
				fmt.Fprintf(os.Stderr, "record-composer: stopping test session %s left by an earlier run\n", s[1])
				stopSession(cfg, string(s[1]))
			}
		}
	}
	home, _ := os.UserHomeDir()
	dirs, _ := filepath.Glob(filepath.Join(home, "fennec-record-composer-*"))
	for _, d := range dirs {
		os.Remove(d) // only if empty
	}
}

func hexBytes(b []byte) []string {
	out := make([]string, len(b))
	for i, c := range b {
		out[i] = fmt.Sprintf("%02x", c)
	}
	return out
}

// ready markers: the composer is drawn and accepts keys.
var ready = map[string]string{
	"claude":   "bypass permissions",
	"codex":    "›",
	"opencode": "Ask anything",
	"grok":     "❯",
}

// waitReady answers the folder trust dialogs (they only confirm the working
// directory) and waits for the composer.
func waitReady(agent string, screen func() string, send func(...string)) error {
	start := time.Now()
	deadline := start.Add(90 * time.Second)
	last, same := "", 0
	for time.Now().Before(deadline) {
		s := screen()
		if s == last && strings.TrimSpace(s) != "" {
			same++
		} else {
			last, same = s, 0
		}
		switch {
		case ready[agent] == "":
			// No known marker (an agent without a profile yet): wait until
			// the screen has settled.
			if same >= 3 && time.Since(start) > 10*time.Second {
				return nil
			}
		case strings.Contains(s, "Do you trust the contents"): // Grok
			send("79")
			time.Sleep(3 * time.Second)
			continue
		case strings.Contains(s, "Yes, I trust this folder"): // Claude Code
			send("1b", "5b", "42")
			time.Sleep(500 * time.Millisecond)
			send("0d")
			time.Sleep(3 * time.Second)
			continue
		case strings.Contains(s, ready[agent]):
			time.Sleep(4 * time.Second) // startup animations settle
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("%s did not show its composer within 90 s", agent)
}

type run struct {
	agent, scenario, dir, session string
	cols, rows                    int
	typed                         map[rune]bool // letters the scenario types or pastes
	err                           error
	report, base                  *predict.ReplayReport
	private                       []string
	saved                         string
}

// check replays the recording, scans it for private text and replays the
// committed recording of the same scenario, if any.
func (r *run) check(cfg config) {
	tr, err := predict.LoadTrace(r.dir)
	if err != nil {
		r.err = err
		return
	}
	if r.session != "" {
		allowed := map[rune]bool{}
		for r := range r.typed {
			allowed[r] = true
		}
		for _, b := range agentBrand[r.agent] {
			allowed[b] = true
		}
		r.private = privateText(tr, r.session, allowed)
	}
	if predict.ProfileFor(r.agent) == nil {
		return // recorded to build a profile from; nothing to replay yet
	}
	profile := func() predict.Profile { return predict.ProfileFor(r.agent) }
	rep := predict.Replay(tr, cfg.rows, cfg.cols, profile, false)
	r.report = &rep
	if b, err := predict.LoadTrace(filepath.Join(cfg.baseline, r.name(cfg))); err == nil && r.session != "" {
		base := predict.Replay(b, cfg.rows, cfg.cols, profile, false)
		r.base = &base
	}
}

func (r *run) name(cfg config) string {
	return fmt.Sprintf("%s-%s-%dx%d", r.agent, r.scenario, cfg.cols, cfg.rows)
}

func (r *run) saveTo(cfg config) {
	if len(r.private) > 0 {
		return
	}
	dest := filepath.Join(cfg.baseline, r.name(cfg))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		r.err = err
		return
	}
	for _, f := range []string{"output", "timing", "input", "acks"} {
		data, err := os.ReadFile(filepath.Join(r.dir, f))
		if err == nil {
			err = os.WriteFile(filepath.Join(dest, f), data, 0o644)
		}
		if err != nil {
			r.err = err
			return
		}
	}
	r.saved = dest
}

var sessionPath = regexp.MustCompile(`\.fen/([a-z]+-[a-z]+)`)

// typedRunes is the set of non-ASCII letters a scenario types or pastes.
func typedRunes(steps []step) map[rune]bool {
	set := map[rune]bool{}
	for _, s := range steps {
		for _, r := range s.typed + s.commit + s.paste {
			if r > unicode.MaxASCII {
				set[r] = true
			}
		}
	}
	return set
}

// privateText lists what in the recorded output may identify the user or
// their other work. Non-ASCII letters the scenario did not type come from
// the agent (history, mention popups, task titles).
func privateText(tr predict.Trace, session string, typed map[rune]bool) []string {
	var out bytes.Buffer
	for _, c := range tr.Output {
		out.Write(c.Data)
	}
	data := out.Bytes()
	lower := bytes.ToLower(data)
	var found []string
	home, _ := os.UserHomeDir()
	needles := []string{home}
	if u, err := user.Current(); err == nil {
		needles = append(needles, u.Username)
	}
	if h, err := os.Hostname(); err == nil {
		needles = append(needles, h, strings.Split(h, ".")[0])
	}
	for _, n := range needles {
		if len(n) >= 3 && bytes.Contains(lower, bytes.ToLower([]byte(n))) {
			found = append(found, fmt.Sprintf("contains %q", n))
		}
	}
	others := map[string]bool{}
	for _, m := range sessionPath.FindAllSubmatch(data, -1) {
		if s := string(m[1]); s != session {
			others[s] = true
		}
	}
	for s := range others {
		found = append(found, fmt.Sprintf("names another session (%s)", s))
	}
	letters := 0
	for i := 0; i < len(data); {
		r, n := utf8.DecodeRune(data[i:])
		if r > unicode.MaxASCII && unicode.IsLetter(r) && !typed[r] {
			letters++
		}
		i += n
	}
	if letters > 0 {
		found = append(found, fmt.Sprintf("%d non-ASCII letters not typed (history, popups or titles?)", letters))
	}
	sort.Strings(found)
	return found
}

// report prints one row per recording and returns the exit status: 1 when a
// profile no longer matches (a wrong prediction, a rollback, or a composer
// never recognised) or a recording failed.
func report(runs []run, cfg config) int {
	status := 0
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tVERSION\tSCENARIO\tSIZE\tKEYS\tPREDICTED\tWRONG\tROLLBACKS\tWAITED\tBASELINE\tRESULT")
	for _, r := range runs {
		if r.err != nil {
			fmt.Fprintf(w, "%s\t\t%s\t%dx%d\t\t\t\t\t\t\terror: %v\n", r.agent, r.scenario, r.cols, r.rows, r.err)
			status = 1
			continue
		}
		rep := r.report
		if rep == nil {
			result := "recorded (no profile to replay)"
			if len(r.private) > 0 {
				result += "; not saved, review: " + strings.Join(r.private, ", ")
				status = 1
			} else if r.saved != "" {
				result += "; saved " + r.saved
			}
			fmt.Fprintf(w, "%s\t\t%s\t%dx%d\t\t\t\t\t\t\t%s\n", r.agent, r.scenario, r.cols, r.rows, result)
			continue
		}
		result := "ok"
		switch {
		case !rep.Stats.Locked:
			result, status = "DRIFT: composer never recognised", 1
		case !rep.OK() || rep.Stats.Diverged > 0:
			result, status = "DRIFT: predictions differ from the agent", 1
		}
		if len(r.private) > 0 {
			result += "; not saved, review: " + strings.Join(r.private, ", ")
			status = 1
		} else if r.saved != "" {
			result += "; saved " + r.saved
		}
		baseline := "-"
		if r.base != nil {
			baseline = fmt.Sprintf("%d predicted", r.base.Predicted)
			if extra := newWaits(rep.Stats.BarrierKeys, r.base.Stats.BarrierKeys); extra != "" {
				baseline += ", now waits: " + extra
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%dx%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", r.agent, orUnknown(rep.Stats.Version), r.scenario, r.cols, r.rows, rep.Keys, rep.Predicted,
			len(rep.Problems), rep.Stats.Diverged, waits(rep.Stats.BarrierKeys), baseline, result)
	}
	w.Flush()
	for _, r := range runs {
		if r.report != nil && len(r.report.Problems) > 0 {
			fmt.Printf("\n%s %s (%s):\n", r.agent, r.scenario, r.dir)
			problems := r.report.Problems
			if !cfg.verbose && len(problems) > 5 {
				problems = append(problems[:5:5], fmt.Sprintf("... %d more (-v)", len(r.report.Problems)-5))
			}
			for _, p := range problems {
				fmt.Println("  " + p)
			}
		}
	}
	return status
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func waits(m map[string]int) string {
	var keys []string
	for k, n := range m {
		keys = append(keys, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "-"
	}
	return strings.Join(keys, ", ")
}

// newWaits lists keys that wait more often than in the baseline.
func newWaits(now, base map[string]int) string {
	var out []string
	for k, n := range now {
		if n > base[k] {
			out = append(out, fmt.Sprintf("%s +%d", k, n-base[k]))
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// agentBrand lists letters an agent draws itself (its logo), which are not
// private text.
var agentBrand = map[string]string{"pi": "π"}
