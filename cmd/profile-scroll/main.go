// profile-scroll measures what scrolling a full-screen agent costs on the
// wire: it runs a stand-in pager (alternate screen, mouse tracking, three
// lines per wheel step, a full coloured redraw, like the agents' own
// transcript views) in a Herdr terminal, attaches through herdr-core attach
// as Fennec does, injects a wheel gesture with a given cadence, and counts
// the attachment's output (what fennec-terminal streams to the Mac).
//
//	profile-scroll [-steps 40] [-gap 16ms] [-rtt 250ms] [-merge 0,8ms,16ms,33ms]
//
// No agent and no prompt are involved. Linux (cloud runtime) only.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"fennec/internal/terminalstream"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const wheelUp = "\x1b[<64;50;15M"

// existing is a running terminal to measure (-address).
var existing string

// resetDown wheel steps down are sent before the gesture (-reset).
var resetDown int

const wheelDown = "\x1b[<65;50;15M"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pager" {
		pager(os.Args[2:])
		return
	}
	steps := flag.Int("steps", 40, "wheel steps in the gesture")
	gap := flag.Duration("gap", 16*time.Millisecond, "time between wheel events (a trackpad delivers about one per frame)")
	rtt := flag.Duration("rtt", 250*time.Millisecond, "round trip: with coalescing, events are sent once per round trip")
	command := flag.String("command", "", "measure this program (run by bash) instead of the stand-in pager, e.g. a resumed agent session")
	startup := flag.Duration("startup", 1500*time.Millisecond, "time for the program to draw before the gesture (then output must be quiet for a second)")
	flag.IntVar(&resetDown, "reset", 0, "wheel steps down sent before each gesture (back to the bottom of a transcript)")
	merge := flag.String("merge", "0,8ms,16ms,33ms", "also replay each gesture's output through time-based merging with these intervals (0: every read as it is)")
	rate := flag.Float64("rate", 112*1024, "link rate in bytes per second for the sender model (the QoS pacer's rate)")
	flag.StringVar(&existing, "address", "", "measure this running Herdr terminal (t…) instead; it is left running")
	flag.Parse()
	self, err := os.Executable()
	if err != nil {
		fail(err)
	}
	policies := []string{"per-event", "per-frame"}
	if *command != "" || existing != "" {
		policies = []string{"command"}
	}
	var intervals []time.Duration
	for _, s := range strings.Split(*merge, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if s == "0" {
			s = "0s"
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			fail(fmt.Errorf("-merge: %w", err))
		}
		intervals = append(intervals, d)
	}
	type run struct {
		label string
		r     result
	}
	var runs []run
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROGRAM\tINPUT\tWRITES\tOUTPUT BYTES\tAFTER NOOP FILTER\tOUTPUT READS\tLAST OUTPUT AFTER\tWHEEL STEPS")
	for _, policy := range policies {
		for _, coalesce := range []bool{false, true} {
			r, err := measure(self, policy, *command, *startup, schedule(*steps, *gap, *rtt, coalesce))
			if err != nil {
				fail(err)
			}
			input := "one per event"
			if coalesce {
				input = "coalesced per RTT"
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%v\t%d\n", policy, input, r.writes, r.bytes, r.filtered, r.reads, r.last.Round(time.Millisecond), r.scrolled)
			runs = append(runs, run{policy + ", " + input, r})
		}
	}
	w.Flush()
	if len(intervals) == 0 {
		return
	}
	// What the cloud would send for the same output: its conflator replayed
	// with a modelled sender and link (-rate, -rtt), with no window and with
	// the live sender's window for -rtt, taking at most once per interval.
	fmt.Println()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tWINDOW\tMERGE INTERVAL\tFRAMES\tBYTES\tMERGED FRAMES\tTHEIR BYTES\tSENT FOR THEM\tLAST ARRIVAL")
	for _, rn := range runs {
		for _, window := range []int{0, terminalstream.SimWindow(*rtt)} {
			for _, d := range intervals {
				m := terminalstream.SimulateSender(30, 100, rn.r.output, rn.r.gesture, terminalstream.SimSender{Interval: d, Window: window, RTT: *rtt, Rate: *rate})
				fmt.Fprintf(w, "%s\t%d\t%v\t%d\t%d\t%d\t%d\t%d\t%v\n", rn.label, window, d, m.Frames, m.Bytes, m.Merged, m.MergedBytes, m.DiffBytes, m.Arrived.Round(time.Millisecond))
			}
		}
	}
	w.Flush()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "profile-scroll:", err)
	os.Exit(1)
}

// write is a burst of wheel events written at once, at offset at.
type write struct {
	at    time.Duration
	steps int
}

// schedule spreads steps events gap apart; coalesced, the events that arrive
// within one round trip are sent together when the previous batch would have
// been acknowledged (the first one at once).
func schedule(steps int, gap, rtt time.Duration, coalesce bool) []write {
	var out []write
	if !coalesce {
		for i := 0; i < steps; i++ {
			out = append(out, write{at: time.Duration(i) * gap, steps: 1})
		}
		return out
	}
	out = append(out, write{at: 0, steps: 1})
	sent, next := 1, rtt
	for sent < steps {
		ready := min(steps, int(next/gap)+1) // events produced by then
		if ready > sent {
			out = append(out, write{at: next, steps: ready - sent})
			sent = ready
		}
		next += rtt
	}
	return out
}

type result struct {
	writes, bytes, filtered, reads, scrolled int
	last                                     time.Duration
	// output: everything read since attaching, after the no-op filter, timed
	// from attaching; gesture: when the gesture began.
	output  []terminalstream.TimedOutput
	gesture time.Duration
}

var addressRe = regexp.MustCompile(`"address"\s*:\s*"(t[0-9a-z]+)"`)

func measure(self, policy, command string, startup time.Duration, plan []write) (result, error) {
	home, _ := os.UserHomeDir()
	hc := filepath.Join(home, ".local/bin/herdr-core")
	address := existing
	if address == "" {
		name := fmt.Sprintf("profile-scroll-%d", os.Getpid())
		argv := []string{self, "pager", policy}
		if command != "" {
			argv = []string{"bash", "-c", command}
		}
		out, err := exec.Command(hc, append([]string{"run", "--bg", "--json", "--no-parent", "--outlive-parent", "--name", name, "--label", "fennec.test=profile-scroll", "--"}, argv...)...).Output()
		if err != nil {
			return result{}, fmt.Errorf("herdr run: %w", err)
		}
		m := addressRe.FindSubmatch(out)
		if m == nil {
			return result{}, fmt.Errorf("herdr run returned no address: %s", out)
		}
		address = string(m[1])
		defer func() {
			exec.Command(hc, "kill", address).Run()
			time.Sleep(300 * time.Millisecond)
			exec.Command(hc, "rm", address).Run()
		}()
		time.Sleep(time.Second)
	}
	attach := exec.Command(hc, "attach", address)
	attach.Env = append(os.Environ(), "TERM=xterm-256color")
	file, err := pty.StartWithSize(attach, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		return result{}, err
	}
	defer file.Close()
	var mu sync.Mutex
	var total, filtered, reads int
	var noop terminalstream.NoopFrameFilter // what the cloud now streams
	var start, lastAt, lastAny time.Time
	var output []terminalstream.TimedOutput
	attached := time.Now()
	counting := false
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				mu.Lock()
				lastAny = time.Now()
				kept, _ := noop.Filter(append([]byte(nil), buf[:n]...))
				if len(kept) > 0 {
					output = append(output, terminalstream.TimedOutput{At: lastAny.Sub(attached), Data: kept})
				}
				if counting {
					total += n
					filtered += len(kept)
					reads++
					lastAt = time.Now()
				}
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	time.Sleep(startup) // initial screen
	if resetDown > 0 {
		// Back to the bottom first, so every gesture really scrolls.
		file.Write([]byte(strings.Repeat(wheelDown, resetDown)))
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		mu.Lock()
		quiet := time.Since(lastAny) > time.Second
		mu.Unlock()
		if quiet {
			break
		}
	}
	mu.Lock()
	counting, start = true, time.Now()
	mu.Unlock()
	steps := 0
	for _, wr := range plan {
		time.Sleep(time.Until(start.Add(wr.at)))
		file.Write([]byte(strings.Repeat(wheelUp, wr.steps)))
		steps += wr.steps
	}
	// Wait for output to stay quiet for a second.
	for {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		quiet := time.Since(lastAt) > time.Second && time.Since(start) > time.Second
		mu.Unlock()
		if quiet {
			break
		}
	}
	file.Write([]byte{0x1c})
	mu.Lock()
	defer mu.Unlock()
	r := result{writes: len(plan), bytes: total, filtered: filtered, reads: reads, scrolled: steps, output: output, gesture: start.Sub(attached)}
	if !lastAt.IsZero() {
		r.last = lastAt.Sub(start)
	}
	return r, nil
}

// pager is the stand-in full-screen program. per-event redraws after every
// wheel report; per-frame redraws at most every 16 ms, after the input that
// arrived meanwhile (as Ink-based agents do).
func pager(args []string) {
	policy := "per-event"
	if len(args) > 0 {
		policy = args[0]
	}
	state, err := term.MakeRaw(0)
	if err == nil {
		defer term.Restore(0, state)
	}
	cols, rows, err := term.GetSize(1)
	if err != nil {
		cols, rows = 100, 30
	}
	const total = 3000
	top := total - rows // start at the bottom, like a transcript
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	out.WriteString("\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h\x1b[?25l")
	render := func() {
		var b bytes.Buffer
		b.WriteString("\x1b[?2026h\x1b[H")
		for r := 0; r < rows; r++ {
			n := top + r
			line := fmt.Sprintf("\x1b[38;5;%dm%5d \x1b[0m\x1b[38;5;250mtranscript line %d: the agent explained a change in some detail here", 30+n%200, n, n)
			b.WriteString(line)
			b.WriteString("\x1b[K")
			if r < rows-1 {
				b.WriteString("\r\n")
			}
		}
		b.WriteString("\x1b[?2026l")
		out.Write(b.Bytes())
		out.Flush()
	}
	_ = cols
	render()
	in := make(chan []byte, 1024)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				in <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(in)
				return
			}
		}
	}()
	report := regexp.MustCompile(`\x1b\[<(\d+);\d+;\d+[Mm]`)
	dirty := false
	tick := time.NewTicker(16 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case data, ok := <-in:
			if !ok {
				return
			}
			for _, m := range report.FindAllSubmatch(data, -1) {
				button, _ := strconv.Atoi(string(m[1]))
				switch button &^ 28 { // ignore modifier bits
				case 64:
					top = max(0, top-3)
				case 65:
					top = min(total-rows, top+3)
				default:
					continue
				}
				if policy == "per-event" {
					render()
				} else {
					dirty = true
				}
			}
		case <-tick.C:
			if dirty {
				dirty = false
				render()
			}
		}
	}
}
