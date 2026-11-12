package flows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/protocol"
)

// ChainAge is how long a chain waits for the next plan or a save.
const ChainAge = 10 * time.Minute

// ErrNothingToSave is a save of the last chain when there is none to save.
var ErrNothingToSave = errors.New("nothing to save: the last plan of the last 10 minutes must start at an app (launch) step, pass or stop in a window that is still in front, and click no target found by vision")

// shortcutVerbs name what a key shortcut does, for automatic flow names.
var shortcutVerbs = map[string]string{"cmd+n": "new", "cmd+s": "save", "cmd+shift+s": "save as", "cmd+o": "open"}

// Chain is the record of the latest chain of passing plans: the steps that
// ran, the window they ended in and the values they used. Password values
// are never in it.
type Chain struct {
	ID string `json:"id"`
	At int64  `json:"at"` // unix ms of the plan that last touched it
	// Window and PID are where the chain ended; a plan starting there
	// within ChainAge extends it.
	Window string            `json:"window"`
	PID    int               `json:"pid,omitempty"`
	Name   string            `json:"name,omitempty"`
	Named  bool              `json:"named,omitempty"`
	Params map[string]string `json:"params"`
	Steps  []protocol.Step   `json:"steps"`
	// Dirty marks a chain a plan is acting on (or one with a step that must
	// not be saved): it is never saved, so a killed run never chains over
	// the steps it took.
	Dirty     bool `json:"dirty"`
	Saved     bool `json:"saved,omitempty"`
	Escalated bool `json:"escalated,omitempty"`
}

// Window is a front window as the plan executor read it.
type Window struct {
	Title string
	PID   int
	// Dialog is set when the window is a dialog or alert that blocks it.
	Dialog bool
}

// Chains keeps the chain record of a flow store.
type Chains struct {
	Store *Store
	// Age overrides ChainAge.
	Age time.Duration
	// Front reads the front window, which must still be the one a stopped
	// chain ended in before that chain is saved; nil skips the check.
	Front func(ctx context.Context) (Window, error)

	now func() time.Time
}

func (c *Chains) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	if c.Store != nil {
		return c.Store.clock()
	}
	return time.Now()
}

func (c *Chains) age() time.Duration {
	if c.Age > 0 {
		return c.Age
	}
	return ChainAge
}

func (c *Chains) fresh(ch *Chain) bool {
	return ch != nil && c.clock().Sub(time.UnixMilli(ch.At)) < c.age()
}

// Pending reads the chain record; nil when there is none.
func (c *Chains) Pending() *Chain {
	if c.Store == nil {
		return nil
	}
	data, err := os.ReadFile(c.Store.path(PendingFile))
	if err != nil {
		return nil
	}
	var ch *Chain
	if json.Unmarshal(data, &ch) != nil {
		return nil
	}
	return ch
}

// put writes the chain record sealed: each password step keeps a
// parameter in place of its text and no intent, and no password value of
// the chain or of plan stays among its params.
func (c *Chains) put(ch *Chain, plan []protocol.Step) error {
	if err := c.Store.ensure(); err != nil {
		return err
	}
	var body []byte
	if ch == nil {
		body = []byte("null")
	} else {
		sealed := *ch
		named, _ := templated(ch.Steps, ch.Params)
		sealed.Steps = make([]protocol.Step, len(ch.Steps))
		for i, s := range ch.Steps {
			if Secure(s) {
				s = named[i]
			}
			sealed.Steps[i] = s
		}
		sealed.Params = unsecret(append(append([]protocol.Step(nil), sealed.Steps...), plan...), ch.Params)
		var err error
		if body, err = json.Marshal(&sealed); err != nil {
			return err
		}
	}
	return c.Store.writeFile(PendingFile, body)
}

// Plan is a plan as the executor starts it.
type Plan struct {
	Steps  []protocol.Step
	Params map[string]string
	// Flow names the saved flow this plan replays.
	Flow string
	// Save names the flow a passing plan is saved as; empty saves it under
	// an automatic name.
	Save string
	// Start is the first step that runs (a continued plan).
	Start int
	// Front is the front window when the plan starts.
	Front Window
}

// Outcome is how a plan ended.
type Outcome struct {
	// Done counts the steps before the one that stopped (all steps when it
	// passed).
	Done int
	OK   bool
	// Settled is set when the plan passed and left no new dialog in front.
	Settled bool
	// Unsaved marks a plan with a step that must not be saved, such as a
	// click whose target only vision found.
	Unsaved bool
	// Front is the front window when it ended (the focused one after a stop).
	Front Window
}

// Ended answers a plan's chain and the flow it was saved as.
type Ended struct {
	Chain   string
	SavedAs string
}

// Run is one plan between Begin and End.
type Run struct {
	c    *Chains
	plan Plan
	held *Chain
}

// Begin marks the chain record dirty before the plan acts, so a run killed
// midway never chains over its steps.
func (c *Chains) Begin(p Plan) (*Run, error) {
	if c == nil || c.Store == nil {
		return nil, ErrNoStore
	}
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	held := c.Pending()
	if held != nil && !held.Dirty {
		dirty := *held
		dirty.Dirty = true
		if err := c.put(&dirty, nil); err != nil {
			return nil, err
		}
	}
	return &Run{c: c, plan: p, held: held}, nil
}

// End records the plan: a plan starting at an app step starts a chain; one
// starting where the last chain ended, within the chain age, extends it (a
// plan after a stopped flow so repairs that flow). A passing chain is
// saved as a flow; the record keeps where it ended.
func (r *Run) End(o Outcome) (Ended, error) {
	c := r.c
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	p := r.plan
	now := c.clock()
	done := o.Done
	if done > len(p.Steps) {
		done = len(p.Steps)
	}
	if p.Flow != "" {
		var err error
		if o.OK {
			err = c.put(nil, p.Steps)
		} else {
			params := cloneParams(p.Params)
			err = c.put(&Chain{ID: newID(now), At: now.UnixMilli(), Window: o.Front.Title, PID: o.Front.PID, Name: Slug(p.Flow), Named: true,
				Params: params, Steps: append([]protocol.Step(nil), p.Steps[:done]...), Dirty: o.Unsaved, Escalated: true}, p.Steps)
		}
		out := Ended{}
		if o.Settled && !o.Unsaved {
			out.SavedAs = Slug(p.Flow)
		}
		return out, err
	}
	start := p.Start
	if start > done {
		start = done
	}
	ran := append([]protocol.Step(nil), p.Steps[start:done]...)
	prev := r.held
	var ch *Chain
	switch {
	case start == 0 && len(p.Steps) > 0 && IsApp(p.Steps[0]):
		ch = &Chain{ID: newID(now), Params: cloneParams(p.Params), Steps: ran, Dirty: o.Unsaved}
	case c.fresh(prev) && ((prev.Window != "" && prev.Window == p.Front.Title) || (prev.PID != 0 && prev.PID == p.Front.PID)):
		ext := *prev
		ext.Params = cloneParams(prev.Params)
		for k, v := range p.Params {
			ext.Params[k] = v
		}
		ext.Steps = append(append([]protocol.Step(nil), prev.Steps...), ran...)
		ext.Dirty = prev.Dirty || o.Unsaved
		ch = &ext
	}
	out := Ended{}
	if ch == nil {
		if prev != nil {
			restored := *prev
			restored.Saved, restored.Escalated = false, false
			return out, c.put(&restored, p.Steps)
		}
		return out, nil
	}
	if p.Save != "" {
		ch.Name, ch.Named = Slug(p.Save), true
	}
	ch.Escalated, ch.Saved = !o.OK, false
	if (o.Settled || (o.OK && changes(ch.Steps))) && !ch.Dirty && hasAction(ch.Steps) {
		name, err := c.store(ch)
		if err != nil {
			return out, err
		}
		out.SavedAs, ch.Saved = name, name != ""
	}
	ch.At = now.UnixMilli()
	ch.Window = o.Front.Title
	if o.Front.PID != 0 {
		ch.PID = o.Front.PID
	}
	out.Chain = ch.ID
	return out, c.put(ch, p.Steps)
}

// changes reports whether steps change a value: typing, setting, or a
// menu toggle.
func changes(steps []protocol.Step) bool {
	for _, s := range steps {
		if s.Set != nil || s.Type != nil || (s.Menu != "" && s.Value != nil) {
			return true
		}
	}
	return false
}

func hasAction(steps []protocol.Step) bool {
	for _, s := range steps {
		if !IsApp(s) {
			return true
		}
	}
	return false
}

func newID(t time.Time) string { return "chain-" + strconv.FormatInt(t.UnixNano(), 36) }

// autoName names a chain by its app and its last two targets.
func autoName(steps []protocol.Step) string {
	var parts []string
	if len(steps) > 0 && steps[0].Launch != "" {
		parts = append(parts, steps[0].Launch)
	}
	var targets []string
	for _, s := range steps {
		switch {
		case s.Menu != "":
			ms := strings.Split(s.Menu, ">")
			targets = append(targets, ms[len(ms)-1])
		case s.Set != nil:
			targets = append(targets, selectorName(s.Set.Select))
		case s.Click != nil:
			targets = append(targets, selectorName(targetText(s.Click)))
		case s.Press != nil:
			targets = append(targets, selectorName(targetText(s.Press)))
		case shortcutVerbs[strings.ToLower(s.Key)] != "":
			targets = append(targets, shortcutVerbs[strings.ToLower(s.Key)])
		}
	}
	if len(targets) > 2 {
		targets = targets[len(targets)-2:]
	}
	var words []string
	for _, t := range append(parts, targets...) {
		w := strings.Fields(t)
		if len(w) > 3 {
			w = w[:3]
		}
		words = append(words, w...)
	}
	return Slug(strings.Join(words, " "))
}

// store saves a chain as a flow: under its name when it was named, else
// under an automatic name that steps around flows saved by name (with a
// -2, -3 suffix that still fits) and replaces the chain's own earlier
// automatic save.
func (c *Chains) store(ch *Chain) (string, error) {
	steps, values := templated(ch.Steps, ch.Params)
	if !ch.Named {
		base := autoName(ch.Steps)
		auto := base
		for n := 2; base != "" && auto != ch.Name && c.Store.named(auto); n++ {
			suffix := "-" + strconv.Itoa(n)
			cut := base
			if len(cut) > MaxSlug-len(suffix) {
				cut = cut[:MaxSlug-len(suffix)]
			}
			auto = Slug(cut + suffix)
		}
		if auto != "" && ch.Name != "" && auto != ch.Name {
			c.Store.remove(ch.Name)
		}
		ch.Name = auto
	}
	if ch.Name == "" {
		return "", nil
	}
	f := &Saved{Name: ch.Name, Named: ch.Named, Params: flowParams(steps, unsecret(steps, values)), Steps: steps, Idempotent: Idempotent(ch.Steps),
		SavedAt: c.clock().UTC().Format(time.RFC3339Nano)}
	if err := c.Store.saveLocked(f, fmt.Sprintf("saved from %d passing steps", len(ch.Steps))); err != nil {
		return "", err
	}
	return ch.Name, nil
}

// SaveLast saves the last chain under name, merging params into its
// values. A chain that stopped is saved only while the window it ended in
// is still in front; rerun asks the same of a chain that passed.
func (c *Chains) SaveLast(ctx context.Context, name string, params map[string]string, rerun bool) (*Saved, error) {
	if c == nil || c.Store == nil {
		return nil, ErrNoStore
	}
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	ch := c.Pending()
	if ch == nil || !c.fresh(ch) || !(ch.Saved || ch.Escalated) || ch.Dirty || !hasAction(ch.Steps) || Slug(name) == "" {
		return nil, ErrNothingToSave
	}
	if (ch.Escalated || rerun) && c.Front != nil {
		w, err := c.Front(ctx)
		if err != nil {
			return nil, err
		}
		if w.Title != ch.Window || w.Dialog {
			front := w.Title
			if front == "" {
				front = "no window"
			}
			return nil, fmt.Errorf("the last plan stopped at a surprise and %q is in front: continue it with act from the failing step, and the passing run is saved", front)
		}
	}
	old := ""
	if ch.Saved && !ch.Named {
		old = ch.Name
	}
	ch.Name, ch.Named = Slug(name), true
	if ch.Params == nil {
		ch.Params = map[string]string{}
	}
	for k, v := range params {
		ch.Params[k] = v
	}
	if old != "" && old != ch.Name {
		c.Store.remove(old)
	}
	if _, err := c.store(ch); err != nil {
		return nil, err
	}
	ch.Saved, ch.Escalated = true, false
	if err := c.put(ch, nil); err != nil {
		return nil, err
	}
	return c.Store.read(ch.Name + ".json")
}

// JustRan reports whether steps, with params, are the steps of the last
// chain, so saving them needs no new run. Password steps compare only by
// whether they end in Return.
func (c *Chains) JustRan(steps []protocol.Step, params map[string]string) bool {
	if c == nil || c.Store == nil {
		return false
	}
	ch := c.Pending()
	if ch == nil || !c.fresh(ch) {
		return false
	}
	was, now := withoutClose(ch.Steps), withoutClose(steps)
	if len(was) != len(now) {
		return false
	}
	for i := range was {
		if bare(was[i], ch.Params) != bare(now[i], params) {
			return false
		}
	}
	return true
}

func withoutClose(steps []protocol.Step) []protocol.Step {
	var out []protocol.Step
	for _, s := range steps {
		if s.Close == "" {
			out = append(out, s)
		}
	}
	return out
}

// bare is a step as it acts, for comparing two plans.
func bare(s protocol.Step, params map[string]string) string {
	f, err := Fill(s, params)
	if err != nil {
		return ""
	}
	f.Intent = ""
	if f.Recorded != nil {
		r := *f.Recorded
		r.Nth, r.Of = 0, 0
		f.Recorded = &r
	}
	if Secure(s) && f.Type != nil {
		ret := "secret"
		if strings.HasSuffix(*f.Type, "\n") {
			ret = "secret\n"
		}
		f.Type = &ret
	}
	b, _ := json.Marshal(f)
	return string(b)
}

// Runner runs a plan for a save, saving it under save when it passes.
type Runner func(ctx context.Context, steps []protocol.Step, params map[string]string, save string) (*protocol.ActResult, error)

// SaveResult answers a save.
type SaveResult struct {
	Flow *Saved
	// Ran says the steps ran again before they were saved.
	Ran bool
	Run *protocol.ActResult
}

// Save stores a flow by name: with no steps (or from "last") the last
// chain; with the steps of the plan that just passed, from the chain
// without running them again; otherwise by running the steps, which saves
// them when they pass.
func (c *Chains) Save(ctx context.Context, name string, steps []protocol.Step, params map[string]string, from string, run Runner) (*SaveResult, error) {
	if c == nil || c.Store == nil {
		return nil, ErrNoStore
	}
	if Slug(name) == "" {
		return nil, fmt.Errorf("flow save needs a name with a letter or digit")
	}
	if from != "" && from != "last" {
		return nil, fmt.Errorf("from takes only \"last\"")
	}
	if from == "last" || len(steps) == 0 {
		f, err := c.SaveLast(ctx, name, params, false)
		if err != nil {
			return nil, err
		}
		return &SaveResult{Flow: f}, nil
	}
	if !Idempotent(steps) && c.JustRan(steps, params) {
		if f, err := c.SaveLast(ctx, name, params, true); err == nil {
			return &SaveResult{Flow: f}, nil
		}
	}
	if run == nil {
		return nil, fmt.Errorf("plans cannot run in this build yet")
	}
	res, err := run(ctx, steps, params, name)
	if err != nil {
		return nil, err
	}
	out := &SaveResult{Ran: true, Run: res}
	if res != nil && res.SavedAs != "" {
		if f, err := c.Store.Load(res.SavedAs); err == nil {
			out.Flow = f
		}
	}
	return out, nil
}
