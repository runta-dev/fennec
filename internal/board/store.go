// Package board keeps projects, their requests and agent-written knowledge on
// the cloud computer, under ~/.fen/board. People post requests from Fennec;
// the Mac daemon hands queued requests to agents (see docs/desktop-app.md);
// agents answer with `fennec-terminal reply`.
package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Request statuses.
const (
	Queued   = "queued"
	Working  = "working"
	NeedsYou = "you"
	// Review: the agent reported its work finished. Only the user's mark
	// done moves it to Resolved; a reply sends it back to the agent.
	Review   = "review"
	Resolved = "resolved"
	Failed   = "failed"
	Unknown  = "unknown"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

// Agents that can work on requests (the cloud runtime's launchable agents).
var Agents = map[string]bool{"claude": true, "codex": true, "pi": true, "opencode": true, "kimi": true, "cursor": true, "grok": true, "agy": true}

type Tag struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Worker struct {
	Agent string `json:"agent"`
	Model string `json:"model,omitempty"`
	On    bool   `json:"on"`
}

type Project struct {
	Deleted     bool      `json:"deleted,omitempty"`
	ID          string    `json:"id"`
	Org         string    `json:"org,omitempty"`
	Key         string    `json:"key"`
	Next        int       `json:"next"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Repository  string    `json:"repository,omitempty"`
	Color       string    `json:"color"`
	Icon        string    `json:"icon,omitempty"`
	Tags        []Tag     `json:"tags"`
	Queue       int       `json:"queue"`
	Paused      bool      `json:"paused"`
	Order       []int     `json:"order,omitempty"`
	Slots       []Slot    `json:"slots,omitempty"`
	Workers     []Worker  `json:"workers"`
	Created     time.Time `json:"created"`
}

type Message struct {
	FollowUp    bool         `json:"follow_up,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	UID         string       `json:"uid,omitempty"`
	RunID       string       `json:"run_id,omitempty"`
	ID          int          `json:"id"`
	Author      string       `json:"author"`
	Agent       string       `json:"agent,omitempty"`
	Body        string       `json:"body"`
	At          time.Time    `json:"at"`
}

type Request struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Tag is the request's first tag; builds before THI-133 know only it.
	Tag string `json:"tag"`
	// Tags are all of the request's tags, Tag first, in the project's tag
	// order; empty while it has just Tag. Read them with TagList.
	Tags []string `json:"tags,omitempty"`
	// TagAuto marks a tag nobody chose: the project's first tag, given when
	// the request was posted without one. The agent working on the request
	// replaces it with the tag that fits (see AutoTag); a person's choice
	// clears it and then stands.
	TagAuto  bool      `json:"tag_auto,omitempty"`
	Status   string    `json:"status"`
	Author   string    `json:"author"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	QueuedAt time.Time `json:"queued_at,omitempty"`
	Failure  string    `json:"failure,omitempty"`
	// The agent and session working on it, once a worker has taken it.
	Agent       string `json:"agent,omitempty"`
	Session     string `json:"session,omitempty"`
	RunLocation string `json:"run_location,omitempty"`
	Runs        []Run  `json:"runs,omitempty"`
	Offer       *Offer `json:"offer,omitempty"`
	Votes       int    `json:"votes,omitempty"`
	// Links are this request's explicit links to other requests of its
	// queue (see links.go); backlinks and mentions are derived.
	Links []Link `json:"links,omitempty"`
	// Reclaim is set when a person marks the request done: its agent sessions
	// are ended once their agents stop. Requests resolved earlier keep theirs.
	Reclaim bool `json:"reclaim,omitempty"`
	// Messages up to this ID have reached Session.
	Delivered int       `json:"delivered,omitempty"`
	Messages  []Message `json:"messages,omitempty"`
}

// Summary is a request without its thread, for lists.
type Summary struct {
	Request
	Replies int      `json:"replies"`
	Latest  *Message `json:"latest,omitempty"`
}

type Store struct {
	Home string
	Now  func() time.Time
	Role string
}

var (
	projectID  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	projectKey = regexp.MustCompile(`^[A-Z0-9]{1,6}$`)
	tagID      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
	color      = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	repository = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	iconData   = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=]+$`)
	orgID      = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)
	sessionRef = regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`)
)

const (
	maxIcon  = 256 << 10
	maxBody  = 64 << 10
	maxTitle = 200
)

// DefaultTags are a new project's tags.
var DefaultTags = []Tag{{"bug", "Bug"}, {"feature", "Feature"}, {"discussion", "Discussion"}}

func (s Store) root() string { return filepath.Join(s.Home, ".fen", "board") }
func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Store) projectDir(id string) string { return filepath.Join(s.root(), "projects", id) }
func (s Store) requestPath(id string, n int) string {
	return filepath.Join(s.projectDir(id), "requests", strconv.Itoa(n)+".json")
}

// lock serializes writers: the Mac, the iPhone and agents. `fennec-terminal reply`.
func (s Store) lock() (func(), error) {
	if err := os.MkdirAll(s.root(), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(s.root(), ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	if err = s.recoverReplica(); err != nil {
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

// ---------- projects ----------

func (s Store) Projects() ([]Project, error) {
	entries, err := os.ReadDir(filepath.Join(s.root(), "projects"))
	if errors.Is(err, os.ErrNotExist) {
		return []Project{}, nil
	}
	if err != nil {
		return nil, err
	}
	projects := []Project{}
	for _, entry := range entries {
		if !entry.IsDir() || !projectID.MatchString(entry.Name()) {
			continue
		}
		if p, err := s.Project(entry.Name()); err == nil {
			projects = append(projects, p)
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Created.Before(projects[j].Created) })
	return projects, nil
}

func (s Store) Project(id string) (Project, error) {
	var p Project
	if !projectID.MatchString(id) {
		return p, ErrNotFound
	}
	err := readJSON(filepath.Join(s.projectDir(id), "project.json"), &p)
	if err == nil && p.Deleted {
		return p, ErrNotFound
	}
	return p, err
}

// DeleteProject persists a replicated tombstone. Existing sessions and files
// remain intact, while the queue and its requests are no longer accessible.
func (s Store) DeleteProject(id string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := s.Project(id)
	if err != nil {
		return err
	}
	p.Deleted = true
	return s.commit(filepath.Join(s.projectDir(id), "project.json"), p)
}

// KeyFrom makes a request-number prefix from a project name: its first
// three letters or digits in capitals, or P when it has none.
func KeyFrom(name string) string {
	var key []rune
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			key = append(key, r)
			if len(key) == 3 {
				break
			}
		}
	}
	if len(key) == 0 {
		return "P"
	}
	return string(key)
}

func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func validProject(p Project) error {
	ok := projectID.MatchString(p.ID) && projectKey.MatchString(p.Key) && orgID.MatchString(p.Org) && p.Next >= 1 &&
		strings.TrimSpace(p.Name) != "" && utf8.RuneCountInString(p.Name) <= 80 && utf8.RuneCountInString(p.Description) <= 300 &&
		(p.Repository == "" || repository.MatchString(p.Repository)) && color.MatchString(p.Color) &&
		(p.Icon == "" || (len(p.Icon) <= maxIcon && iconData.MatchString(p.Icon))) &&
		p.Queue >= 1 && p.Queue <= 10 && len(p.Tags) >= 1 && len(p.Tags) <= 12 && len(p.Workers) >= 1 && len(p.Workers) <= 12
	if !ok {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, t := range p.Tags {
		if !tagID.MatchString(t.ID) || seen[t.ID] || strings.TrimSpace(t.Label) == "" || utf8.RuneCountInString(t.Label) > 24 {
			return ErrInvalid
		}
		seen[t.ID] = true
	}
	on := 0
	for _, w := range p.Workers {
		if !Agents[w.Agent] || utf8.RuneCountInString(w.Model) > 80 || strings.ContainsAny(w.Model, "\x00\r\n") {
			return ErrInvalid
		}
		if w.On {
			on++
		}
	}
	if on == 0 {
		return ErrInvalid
	}
	return nil
}

// CreateProject adds a project with default tags, a queue of 3 and the given
// worker. Its id comes from the name, made unique.
func (s Store) CreateProject(p Project) (Project, error) {
	unlock, err := s.lock()
	if err != nil {
		return p, err
	}
	defer unlock()
	base := slug(p.Name)
	if base == "" {
		base = "project"
	}
	p.ID = base
	if s.Role != "" {
		p.ID = base[:min(len(base), 33)] + "-" + randomID()[:6]
	}
	for i := 2; ; i++ {
		if _, err := os.Stat(s.projectDir(p.ID)); errors.Is(err, os.ErrNotExist) {
			break
		}
		p.ID = fmt.Sprintf("%s-%d", base, i)
	}
	if p.Key == "" {
		p.Key = KeyFrom(p.Name)
	}
	if p.Color == "" {
		p.Color = "#f38d25"
	}
	if len(p.Tags) == 0 {
		p.Tags = append([]Tag(nil), DefaultTags...)
	}
	if p.Queue == 0 {
		p.Queue = 3
	}
	if len(p.Workers) == 0 {
		p.Workers = []Worker{{Agent: "claude", On: true}}
	}
	p.Next, p.Created = 1, s.now()
	if s.Role != "" {
		for i := 0; i < p.Queue; i++ {
			p.Slots = append(p.Slots, Slot{ID: fmt.Sprintf("slot-%d", i), Index: i, Owner: s.Role})
		}
	}
	if err = validProject(p); err != nil {
		return p, err
	}
	return p, s.commit(filepath.Join(s.projectDir(p.ID), "project.json"), p)
}

// UpdateProject applies a partial change. The id, next number and creation
// time never change.
func (s Store) UpdateProject(id string, change json.RawMessage) (Project, error) {
	unlock, err := s.lock()
	if err != nil {
		return Project{}, err
	}
	defer unlock()
	p, err := s.Project(id)
	if err != nil {
		return p, err
	}
	next := p
	if err = json.Unmarshal(change, &next); err != nil {
		return p, ErrInvalid
	}
	next.ID, next.Next, next.Created, next.Order = p.ID, p.Next, p.Created, p.Order
	next.Slots = p.Slots
	next.Deleted = p.Deleted
	if s.Role != "" {
		for i := len(next.Slots); i < next.Queue; i++ {
			next.Slots = append(next.Slots, Slot{ID: fmt.Sprintf("slot-%d", i), Index: i, Owner: s.Role})
		}
	}
	if err = validProject(next); err != nil {
		return p, err
	}
	return next, s.commit(filepath.Join(s.projectDir(id), "project.json"), next)
}

// ---------- requests ----------

func (s Store) Requests(project string) ([]Summary, error) {
	if _, err := s.Project(project); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.projectDir(project), "requests"))
	if errors.Is(err, os.ErrNotExist) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	list := []Summary{}
	for _, entry := range entries {
		n, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		r, err := s.Request(project, n)
		if err != nil {
			continue
		}
		list = append(list, summarize(r))
	}
	p, _ := s.Project(project)
	sortRequests(list, p.Order)
	return list, nil
}

func summarize(r Request) Summary {
	sum := Summary{Request: r, Replies: max(0, len(r.Messages)-1)}
	if len(r.Messages) > 0 {
		latest := r.Messages[len(r.Messages)-1]
		if utf8.RuneCountInString(latest.Body) > 240 {
			latest.Body = string([]rune(latest.Body)[:240])
		}
		sum.Latest = &latest
	}
	sum.Messages = nil
	return sum
}

func (s Store) Request(project string, n int) (Request, error) {
	var r Request
	if !projectID.MatchString(project) || n < 1 {
		return r, ErrNotFound
	}
	err := readJSON(s.requestPath(project, n), &r)
	return r, err
}

func validText(text string, limit int) bool {
	return strings.TrimSpace(text) != "" && len(text) <= limit && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

// CreateRequest posts a request; it waits as Queued for a worker.
func (s Store) CreateRequest(project, author, title, tag, body string) (Request, error) {
	var tags []string
	if tag != "" {
		tags = []string{tag}
	}
	return s.CreateRequestWithAttachments(project, author, title, tags, body, nil)
}

// CreateRequestWithAttachments posts a request with tags (ids or labels); no
// tags leave a placeholder for the agent that takes it (TagAuto).
func (s Store) CreateRequestWithAttachments(project, author, title string, tags []string, body string, images []Attachment) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	attachments, err := s.validateAttachments(images)
	if err != nil {
		return Request{}, err
	}
	p, err := s.Project(project)
	if err != nil {
		return Request{}, err
	}
	title = strings.TrimSpace(title)
	auto := len(tags) == 0
	if auto {
		tags = []string{p.Tags[0].ID}
	}
	tags, ok := TagsFor(p, tags)
	if !ok || !validText(title, maxTitle*4) || utf8.RuneCountInString(title) > maxTitle || strings.ContainsAny(title, "\r\n") ||
		!validText(author, 80) || (body != "" && !validText(body, maxBody)) {
		return Request{}, ErrInvalid
	}
	if body == "" {
		body = title
	}
	now := s.now()
	if s.Role != "" {
		list, err := s.Requests(project)
		if err != nil {
			return Request{}, err
		}
		for _, item := range list {
			if item.Number >= p.Next {
				p.Next = item.Number + 1
			}
		}
		want := 1
		if s.Role == "cloud" {
			want = 0
		}
		if p.Next%2 != want {
			p.Next++
		}
	}
	r := Request{Number: p.Next, Title: title, TagAuto: auto, Status: Queued, Author: author, Created: now, Updated: now, QueuedAt: now,
		Messages: []Message{{UID: randomID(), ID: 1, Author: author, Body: body, At: now, Attachments: attachments}}}
	r.setTags(tags)
	p.Next++
	if err = s.commit(s.requestPath(project, r.Number), r); err != nil {
		return r, err
	}
	return r, s.commit(filepath.Join(s.projectDir(project), "project.json"), p)
}

// Reply adds a comment without scheduling work. An agent response hands the
// request back to the person. Continue is an explicit scheduling action.
func (s Store) Reply(project string, n int, author, agent, body string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if !validText(author, 80) || !validText(body, maxBody) || (agent != "" && !Agents[agent]) || len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	if agent != "" && (r.Status != Working || len(r.Runs) > 1) {
		return r, ErrConflict
	}
	now := s.now()
	id := 1
	if len(r.Messages) > 0 {
		id = r.Messages[len(r.Messages)-1].ID + 1
	}
	message := Message{UID: randomID(), ID: id, Author: author, Agent: agent, Body: body, At: now}
	if agent != "" {
		message.RunID = r.Session
	}
	r.Messages = append(r.Messages, message)
	r.Updated = now
	if agent != "" {
		r.Status = NeedsYou
		r.Reclaim = false
		r.Agent = agent
		r.Delivered = id
		recordRun(&r, NeedsYou, now)
	}
	return r, s.commit(s.requestPath(project, n), r)
}

// Change sets a request's tags (all of them, ids or labels; nil keeps them)
// or status (resolve, reopen). Reopening returns it to Needs you; a reply puts
// it back in the queue.
func (s Store) Change(project string, n int, tags []string, status string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	p, err := s.Project(project)
	if err != nil {
		return Request{}, err
	}
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if tags != nil {
		ids, ok := TagsFor(p, tags)
		if !ok {
			return r, ErrInvalid
		}
		r.setTags(ids)
		r.TagAuto = false
	}
	switch status {
	case "":
	case Resolved:
		if r.Status != NeedsYou && r.Status != Review && r.Status != Failed && r.Status != Resolved {
			return r, ErrConflict
		}
		r.Reclaim = r.Reclaim || r.Status != Resolved
		r.Status = Resolved
	case NeedsYou:
		if r.Status != Resolved {
			return r, ErrConflict
		}
		r.Status, r.Reclaim = NeedsYou, false
	default:
		return r, ErrInvalid
	}
	r.Updated = s.now()
	return r, s.commit(s.requestPath(project, n), r)
}

// Claim gives a queued request to a session. It fails if someone else took
// it first, so two dispatchers never start two agents for one request.
func (s Store) Claim(project string, n int, session, agent string) (Request, error) {
	return s.ClaimAt(project, n, session, agent, "cloud")
}

func (s Store) ClaimAt(project string, n int, session, agent, location string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if !sessionRef.MatchString(session) || !Agents[agent] || (location != "local" && location != "cloud") {
		return r, ErrInvalid
	}
	if r.Status != Queued {
		return r, ErrConflict
	}
	if len(r.Messages) == 0 || !s.AttachmentsReady(r) {
		return r, ErrConflict
	}
	p, err := s.Project(project)
	if err != nil {
		return r, err
	}
	list, err := s.Requests(project)
	if err != nil {
		return r, err
	}
	working, first := 0, 0
	for _, item := range list {
		if item.Status == Working || item.Status == Unknown {
			working++
		}
		if item.Status == Queued && first == 0 {
			first = item.Number
		}
	}
	if p.Paused || working >= p.Queue || first != n {
		return r, ErrConflict
	}
	if err = s.validateDispatch(p, list, n, agent, location, session); err != nil {
		return r, err
	}
	r.Status, r.Session, r.Agent = Working, session, agent
	r.RunLocation = location
	inputs := []string{}
	for _, m := range r.Messages {
		inputs = append(inputs, m.UID)
	}
	r.Runs = append(r.Runs, Run{ID: session, Agent: agent, InputUIDs: inputs, InputAfter: r.Delivered, Session: session, Location: location, State: "starting", Started: s.now()})
	if len(r.Messages) > 0 {
		r.Delivered = r.Messages[len(r.Messages)-1].ID
	}
	r.Updated = s.now()
	return r, s.commit(s.requestPath(project, n), r)
}

// Release returns a claimed request to the queue when its session could not
// start (or has gone), so the next pass starts a new one.
func (s Store) Release(project string, n int, session string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Session != session {
		return r, ErrConflict
	}
	r.Session, r.Delivered = "", 0
	if r.Status == Working {
		r.Status, r.Failure = Failed, "The session could not start"
	}
	r.Updated = s.now()
	return r, s.commit(s.requestPath(project, n), r)
}

// QueueProject is one project's slots and waiting requests, oldest first.
type QueueProject struct {
	Project string    `json:"project"`
	Key     string    `json:"key"`
	Limit   int       `json:"limit"`
	Working int       `json:"working"`
	Paused  bool      `json:"paused"`
	Workers []Worker  `json:"workers"`
	Queued  []Request `json:"queued"`
	Active  []Request `json:"active"`
	// Stalled holds needs-you requests whose agent stopped without replying;
	// they leave the running slots but resume if the agent works again.
	Stalled []Request `json:"stalled,omitempty"`
}

// Queue lists every project's waiting requests with their threads, for the
// dispatcher.
func (s Store) Queue() ([]QueueProject, error) {
	projects, err := s.Projects()
	if err != nil {
		return nil, err
	}
	queue := []QueueProject{}
	for _, p := range projects {
		list, err := s.Requests(p.ID)
		if err != nil {
			continue
		}
		q := QueueProject{Project: p.ID, Key: p.Key, Limit: p.Queue, Paused: p.Paused, Workers: p.Workers, Queued: []Request{}}
		for _, sum := range list {
			if sum.Status == Working || sum.Status == Unknown {
				q.Working++
				if active, err := s.Request(p.ID, sum.Number); err == nil {
					q.Active = append(q.Active, active)
				}
			}
			if sum.Status == NeedsYou {
				if r, err := s.Request(p.ID, sum.Number); err == nil && r.Stalled() {
					q.Stalled = append(q.Stalled, r)
				}
			}
		}
		for i := 0; i < len(list); i++ {
			if list[i].Status == Queued {
				if r, err := s.Request(p.ID, list[i].Number); err == nil {
					q.Queued = append(q.Queued, r)
				}
			}
		}
		queue = append(queue, q)
	}
	return queue, nil
}

// Find resolves a reference such as FEN-42 to its project and number.
func (s Store) Find(ref string) (Project, int, error) {
	dash := strings.LastIndexByte(ref, '-')
	n, err := strconv.Atoi(ref[dash+1:])
	if dash < 1 || err != nil || n < 1 {
		return Project{}, 0, ErrNotFound
	}
	key := strings.ToUpper(ref[:dash])
	projects, err := s.Projects()
	if err != nil {
		return Project{}, 0, err
	}
	for _, p := range projects {
		if p.Key == key {
			if _, err := s.Request(p.ID, n); err == nil {
				return p, n, nil
			}
		}
	}
	return Project{}, 0, ErrNotFound
}
