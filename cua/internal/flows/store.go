package flows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/protocol"
)

// Store files.
const (
	IndexFile   = "INDEX"
	PendingFile = ".pending.json"
	RunLogFile  = ".runs.jsonl"
	// RunLogMax is the size past which the run log starts over.
	RunLogMax = 1 << 20
)

// ErrNoStore is a flow call with no flow store configured.
var ErrNoStore = errors.New("no flow store is configured")

// Saved is a flow as its file holds it.
type Saved struct {
	Name        string                        `json:"name"`
	Description string                        `json:"description,omitempty"`
	Named       bool                          `json:"named,omitempty"` // saved by a name the caller gave
	Params      map[string]protocol.FlowParam `json:"params"`
	Steps       []protocol.Step               `json:"steps"`
	// Idempotent is false when a replay types or presses keys again.
	Idempotent bool   `json:"idempotent"`
	SavedAt    string `json:"saved_at"`
}

// Flow is the saved flow in the protocol's shape.
func (s *Saved) Flow() *protocol.Flow {
	return &protocol.Flow{Name: s.Name, Description: s.Description, Params: s.Params, Steps: s.Steps}
}

// Info is the flow's list entry.
func (s *Saved) Info() protocol.FlowInfo {
	info := protocol.FlowInfo{Name: s.Name, Description: s.Description, Params: sortedKeys(s.Params)}
	for _, st := range s.Steps {
		if st.Launch != "" {
			info.App = st.Launch
			break
		}
	}
	return info
}

// Store is a directory of flows: one JSON file per flow, an INDEX, the
// pending chain, a run log, and a git history where git can run. The
// directory is 0700 and its files 0600; nothing is written outside it.
type Store struct {
	Dir string

	now func() time.Time
	// git runs git in Dir; nil runs the git binary.
	git func(args ...string) error
	// devTools reports whether git may run: on macOS only with the Command
	// Line Tools installed, since the git stub would open their installer.
	devTools func() bool

	mu     sync.Mutex
	gitOK  bool
	gitSet sync.Once
}

// NewStore opens the store in dir; an empty dir is no store.
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, ErrNoStore
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ensure makes the directory and keeps it and its files private, which
// also tightens a directory an older install left readable.
func (s *Store) ensure() error {
	if s == nil || s.Dir == "" {
		return ErrNoStore
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if info, err := e.Info(); err == nil && info.Mode().Perm()&0o077 != 0 {
				_ = os.Chmod(filepath.Join(s.Dir, e.Name()), 0o600)
			}
		}
	}
	return nil
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

// writeFile writes a private file atomically.
func (s *Store) writeFile(name string, data []byte) error {
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(name))
}

// gitWorks says whether git may run: never on macOS without the Command
// Line Tools, and never without a git binary.
func (s *Store) gitWorks() bool {
	s.gitSet.Do(func() {
		dev := s.devTools
		if dev == nil {
			dev = commandLineTools
		}
		ok := dev()
		if ok && s.git == nil {
			_, err := exec.LookPath("git")
			ok = err == nil
		}
		s.gitOK = ok
	})
	return s.gitOK
}

// commandLineTools reports whether git may run: on macOS only with the
// Command Line Tools installed.
func commandLineTools() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "xcode-select", "-p").Run() == nil
}

// runGit runs one git command in the store, never signing and never
// running hooks or prompts; its failure never fails a save.
func (s *Store) runGit(args ...string) {
	if !s.gitWorks() {
		return
	}
	if s.git != nil {
		_ = s.git(args...)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-C", s.Dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	_ = cmd.Run()
}

func (s *Store) repo() {
	if !s.gitWorks() {
		return
	}
	if _, err := os.Stat(s.path(".git")); err == nil {
		return
	}
	s.runGit("init", "-q")
	_ = s.writeFile(".gitignore", []byte(RunLogFile+"\n"+PendingFile+"\n"))
	s.runGit("config", "user.name", "fennec-cua")
	s.runGit("config", "user.email", "fennec-cua@localhost")
}

func (s *Store) commit(msg string) {
	s.runGit("add", "-A")
	s.runGit("commit", "-q", "-m", msg)
}

// Save writes a flow under its name, rebuilds the index and commits.
func (s *Store) Save(f *Saved, why string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(f, why)
}

func (s *Store) saveLocked(f *Saved, why string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	if f.Name == "" || f.Name != Slug(f.Name) {
		return fmt.Errorf("flow name %q is not a slug", f.Name)
	}
	if f.SavedAt == "" {
		f.SavedAt = s.clock().UTC().Format(time.RFC3339Nano)
	}
	if f.Params == nil {
		f.Params = map[string]protocol.FlowParam{}
	}
	s.repo()
	body, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	if err := s.writeFile(f.Name+".json", append(body, '\n')); err != nil {
		return err
	}
	if err := s.writeIndex(); err != nil {
		return err
	}
	s.commit(f.Name + ": " + why)
	return nil
}

// Load reads a flow by name.
func (s *Store) Load(name string) (*Saved, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	slug := Slug(name)
	f, err := s.read(slug + ".json")
	if err != nil {
		var names []string
		all, _ := s.All()
		for _, x := range all {
			names = append(names, x.Name)
		}
		saved := "none"
		if len(names) > 0 {
			saved = strings.Join(names, ", ")
		}
		return nil, fmt.Errorf("no saved flow %q; saved: %s", name, saved)
	}
	return f, nil
}

// remove deletes a flow file without committing.
func (s *Store) remove(name string) {
	if name != "" {
		_ = os.Remove(s.path(Slug(name) + ".json"))
	}
}

// Delete removes a flow, rebuilds the index and commits.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return err
	}
	slug := Slug(name)
	if err := os.Remove(s.path(slug + ".json")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no saved flow %q", name)
		}
		return err
	}
	s.repo()
	if err := s.writeIndex(); err != nil {
		return err
	}
	s.commit(slug + ": deleted")
	return nil
}

// named reports whether a flow was saved by a name the caller gave.
func (s *Store) named(name string) bool {
	f, err := s.read(name + ".json")
	return err == nil && f.Named
}

var required = []string{"name", "saved_at", "params", "steps"}

// read parses one flow file; a file lacking its name, saved time, params
// or steps is malformed.
func (s *Store) read(file string) (*Saved, error) {
	data, err := os.ReadFile(s.path(file))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, k := range required {
		v, ok := raw[k]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, fmt.Errorf("%s has no %s", file, k)
		}
	}
	var f Saved
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Name == "" || f.SavedAt == "" {
		return nil, fmt.Errorf("%s has no name or saved time", file)
	}
	return &f, nil
}

// All reads every well-formed flow, newest first; malformed files are
// left out.
func (s *Store) All() ([]*Saved, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Saved
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".json") {
			continue
		}
		if f, err := s.read(n); err == nil {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SavedAt != out[j].SavedAt {
			return out[i].SavedAt > out[j].SavedAt
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// List answers every flow's list entry, newest first.
func (s *Store) List() ([]protocol.FlowInfo, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	out := make([]protocol.FlowInfo, 0, len(all))
	for _, f := range all {
		out = append(out, f.Info())
	}
	return out, nil
}

// IndexLine is a flow's line in the INDEX: name(params), a once marker
// when a replay types or presses keys again, and a step summary.
func IndexLine(f *Saved) string {
	once := ""
	if !Idempotent(f.Steps) {
		once = " once"
	}
	return fmt.Sprintf("%s(%s)%s: %s", f.Name, strings.Join(sortedKeys(f.Params), ", "), once, Summary(f.Steps))
}

func (s *Store) indexLines() ([]string, []*Saved, error) {
	all, err := s.All()
	if err != nil {
		return nil, nil, err
	}
	lines := make([]string, len(all))
	for i, f := range all {
		lines[i] = IndexLine(f)
	}
	return lines, all, nil
}

func (s *Store) writeIndex() error {
	lines, _, err := s.indexLines()
	if err != nil {
		return err
	}
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	return s.writeFile(IndexFile, []byte(body))
}

// Reindex rebuilds the index from the flow files and commits.
func (s *Store) Reindex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return err
	}
	s.repo()
	if err := s.writeIndex(); err != nil {
		return err
	}
	s.commit("sync from the flow files")
	return nil
}

// Summary describes a flow's acting steps in at most 100 characters.
func Summary(steps []protocol.Step) string {
	var parts []string
	for _, s := range steps {
		switch {
		case s.Launch != "" || s.Wait != nil || s.Close != "" || s.Focus != "":
			continue
		case s.Menu != "":
			parts = append(parts, s.Menu)
		case s.Set != nil:
			v := ""
			if s.Value != nil {
				v = string(*s.Value)
				var str string
				if json.Unmarshal(*s.Value, &str) == nil {
					v = str
				}
			}
			parts = append(parts, s.Set.Select+"="+v)
		case s.Type != nil:
			parts = append(parts, "type "+strings.TrimSuffix(*s.Type, "\n"))
		case s.Key != "":
			parts = append(parts, "key "+s.Key)
		case s.Click != nil:
			parts = append(parts, targetText(s.Click))
		case s.Press != nil:
			parts = append(parts, targetText(s.Press))
		}
	}
	text := strings.Join(parts, ", ")
	if r := []rune(text); len(r) > 100 {
		text = string(r[:99]) + "…"
	}
	return text
}

func targetText(t *protocol.Target) string {
	switch {
	case t == nil:
		return ""
	case t.Select != "":
		return t.Select
	case t.Ref != "":
		return t.Ref
	case t.Point != nil:
		return fmt.Sprintf("%g, %g", t.Point.X, t.Point.Y)
	}
	return ""
}

var idempotentKey = regexp.MustCompile(`(?i)^(esc|escape|tab|shift\+tab)$`)

// Idempotent reports whether a replay of the steps types nothing and
// presses no key but Escape and Tab.
func Idempotent(steps []protocol.Step) bool {
	for _, s := range steps {
		if s.Type != nil || (s.Key != "" && !idempotentKey.MatchString(s.Key)) {
			return false
		}
	}
	return true
}

// LogRun appends one entry to the run log, which starts over once it
// passes RunLogMax.
func (s *Store) LogRun(entry any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return err
	}
	p := s.path(RunLogFile)
	if fi, err := os.Stat(p); err == nil && fi.Size() > RunLogMax {
		_ = os.Remove(p)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
