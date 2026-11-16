// Package predictstats keeps local usage statistics of predictive echo: one
// line per attached session with counters only (no typed text, no screen
// content), so the real hit rate and the keys that still wait for the agent
// can be read per agent and agent version. Nothing leaves the machine.
package predictstats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"fennec/internal/predict"
)

// MaxBytes is the log size at which it is rotated to <name>.1.
const MaxBytes = 2 << 20

// Entry is one attached session.
type Entry struct {
	At           time.Time      `json:"at"`
	Agent        string         `json:"agent"`
	Version      string         `json:"version,omitempty"`
	Rows         int            `json:"rows"`
	Columns      int            `json:"columns"`
	EchoAck      bool           `json:"echo_ack,omitempty"`
	Locked       bool           `json:"locked"`
	Keys         int            `json:"keys"`
	Predicted    int            `json:"predicted"`
	Confirmed    int            `json:"confirmed"`
	Diverged     int            `json:"diverged"`
	Expired      int            `json:"expired"`
	Barriers     int            `json:"barriers"`
	BarrierKeys  map[string]int `json:"barrier_keys,omitempty"`
	Unlocked     int            `json:"unlocked"`
	UnlockedKeys int            `json:"unlocked_keys"`
	ConfirmP50MS float64        `json:"confirm_p50_ms,omitempty"`
	ConfirmP95MS float64        `json:"confirm_p95_ms,omitempty"`
	PaintP95MS   float64        `json:"paint_p95_ms,omitempty"`
	// Test marks a session started by a recording tool (record-composer
	// sets FEN_PREDICT_STATS=test); it is left out of summaries by default.
	Test bool `json:"test,omitempty"`
	// Typed excludes Neutral events (mouse and focus reports) from Keys.
	// Entries written before these fields have only Keys.
	Typed          int            `json:"typed,omitempty"`
	Neutral        int            `json:"neutral,omitempty"`
	Chars          int            `json:"chars,omitempty"`
	Commits        int            `json:"commits,omitempty"` // input method commits
	RollbackCauses map[string]int `json:"rollback_causes,omitempty"`
}

// typed is the number of keys typed (Keys for entries without Typed).
func (e Entry) typed() int {
	if e.Typed > 0 || e.Neutral > 0 {
		return e.Typed
	}
	return e.Keys
}

// Filter keeps entries at or after since (zero: all), without test sessions
// unless includeTest.
func Filter(entries []Entry, since time.Time, includeTest bool) []Entry {
	var out []Entry
	for _, e := range entries {
		if (includeTest || !e.Test) && !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out
}

// Path is the log file under home.
func Path(home string) string { return filepath.Join(home, ".config", "fennec", "predict-stats.jsonl") }

// FromStats builds an entry from an engine's counters.
func FromStats(agent string, rows, columns int, echoAck, test bool, s predict.Stats, at time.Time) Entry {
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return Entry{
		At: at.UTC().Truncate(time.Second), Agent: agent, Version: s.Version, Rows: rows, Columns: columns, EchoAck: echoAck,
		Locked: s.Locked, Keys: s.Keys, Predicted: s.Predicted, Confirmed: s.Confirmed, Diverged: s.Diverged,
		Expired: s.Expired, Barriers: s.Barriers, BarrierKeys: s.BarrierKeys, Unlocked: s.Unlocked,
		UnlockedKeys: s.UnlockedKeys, ConfirmP50MS: ms(s.ConfirmP50), ConfirmP95MS: ms(s.ConfirmP95), PaintP95MS: ms(s.LocalPaintP95),
		Test: test, Typed: s.Typed, Neutral: s.Neutral, Chars: s.Chars, Commits: s.Commits, RollbackCauses: s.RollbackCauses,
	}
}

// Append adds an entry to the log, rotating it when it grows past MaxBytes.
func Append(home string, e Entry) error {
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > MaxBytes {
		_ = os.Rename(path, path+".1")
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Load reads the rotated and the current log, oldest first. Malformed lines
// are skipped.
func Load(home string) ([]Entry, error) {
	var out []Entry
	for _, path := range []string{Path(home) + ".1", Path(home)} {
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			var e Entry
			if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Agent != "" {
				out = append(out, e)
			}
		}
		f.Close()
	}
	return out, nil
}

// Summary aggregates the sessions of one agent version.
type Summary struct {
	Agent          string         `json:"agent"`
	Version        string         `json:"version"`
	Sessions       int            `json:"sessions"`
	NeverLocked    int            `json:"never_locked"` // sessions with keys but no recognised composer
	Typed          int            `json:"typed"`
	Chars          int            `json:"chars"`
	Commits        int            `json:"commits"`
	Predicted      int            `json:"predicted"`
	Confirmed      int            `json:"confirmed"`
	Diverged       int            `json:"diverged"`
	Expired        int            `json:"expired"`
	Barriers       int            `json:"barriers"`
	UnlockedKeys   int            `json:"unlocked_keys"`
	BarrierKeys    map[string]int `json:"barrier_keys,omitempty"`
	RollbackCauses map[string]int `json:"rollback_causes,omitempty"`
	ConfirmP50MS   float64        `json:"confirm_p50_ms"` // median of session medians
	First          time.Time      `json:"first"`
	Last           time.Time      `json:"last"`
}

// Summarize groups entries by agent and version, newest version first.
func Summarize(entries []Entry) []Summary {
	groups := map[[2]string]*Summary{}
	medians := map[[2]string][]float64{}
	for _, e := range entries {
		key := [2]string{e.Agent, e.Version}
		s := groups[key]
		if s == nil {
			s = &Summary{Agent: e.Agent, Version: e.Version, BarrierKeys: map[string]int{}, RollbackCauses: map[string]int{}, First: e.At}
			groups[key] = s
		}
		s.Sessions++
		if !e.Locked && e.typed() > 0 {
			s.NeverLocked++
		}
		s.Typed += e.typed()
		s.Chars += e.Chars
		s.Commits += e.Commits
		for k, n := range e.RollbackCauses {
			s.RollbackCauses[k] += n
		}
		s.Predicted += e.Predicted
		s.Confirmed += e.Confirmed
		s.Diverged += e.Diverged
		s.Expired += e.Expired
		s.Barriers += e.Barriers
		s.UnlockedKeys += e.UnlockedKeys
		for k, n := range e.BarrierKeys {
			s.BarrierKeys[k] += n
		}
		if e.ConfirmP50MS > 0 {
			medians[key] = append(medians[key], e.ConfirmP50MS)
		}
		s.Last = e.At
	}
	out := make([]Summary, 0, len(groups))
	for key, s := range groups {
		if m := medians[key]; len(m) > 0 {
			sort.Float64s(m)
			s.ConfirmP50MS = m[len(m)/2]
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Last.After(out[j].Last)
	})
	return out
}

func percent(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(of))
}

// topKeys lists the most frequent barrier keys.
func topKeys(m map[string]int, n int) string {
	type kv struct {
		k string
		n int
	}
	var list []kv
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n || list[i].n == list[j].n && list[i].k < list[j].k })
	var parts []string
	for i := 0; i < len(list) && i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s %d", list[i].k, list[i].n))
	}
	return strings.Join(parts, ", ")
}

// Write prints the summaries as a table.
//
//	typed:     keys typed (mouse and focus reports excluded); IME: input
//	           method commits among them
//	predicted: keys shown at once / keys typed into a recognised composer
//	rollback:  predictions the agent drew differently or not in time /
//	           predictions; causes: wrong|missing:<key>[:busy]
//	unlocked:  keys typed while no composer was recognised / typed
func Write(w io.Writer, summaries []Summary) error {
	if len(summaries) == 0 {
		_, err := fmt.Fprintf(w, "No sessions recorded yet. Statistics are written when a session detaches.\n")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tVERSION\tSESSIONS\tTYPED\tIME\tPREDICTED\tROLLBACK\tUNLOCKED\tCONFIRM P50\tWAITED FOR AGENT\tROLLBACK CAUSES")
	for _, s := range summaries {
		version := s.Version
		if version == "" {
			version = "?"
		}
		sessions := fmt.Sprint(s.Sessions)
		if s.NeverLocked > 0 {
			sessions += fmt.Sprintf(" (%d never locked)", s.NeverLocked)
		}
		confirm := "-"
		if s.ConfirmP50MS > 0 {
			confirm = fmt.Sprintf("%.0f ms", s.ConfirmP50MS)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Agent, version, sessions, s.Typed, s.Commits,
			percent(s.Predicted, s.Predicted+s.Barriers), percent(s.Diverged, s.Predicted),
			percent(s.UnlockedKeys, s.Typed), confirm, topKeys(s.BarrierKeys, 5), topKeys(s.RollbackCauses, 3))
	}
	return tw.Flush()
}
