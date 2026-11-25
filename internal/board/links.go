package board

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Requests link to each other the way GitHub issues do (THI-103):
//
//   - Mentioning another request of the same queue by its reference
//     ("THI-42") in any message is a link. Nothing is stored for it.
//   - An explicit link is stored on the request that makes it (Links):
//     "related", or "duplicate" for a request merged into another one.
//
// Every link shows on both threads: on its source as a link, on its target as
// a backlink. Backlinks are derived, never stored, so they can't go stale.

// Link kinds.
const (
	LinkRelated   = "related"
	LinkDuplicate = "duplicate"
)

// Link is an explicit link from the request that holds it to another request
// of the same queue.
type Link struct {
	Number int       `json:"number"`
	Kind   string    `json:"kind"`
	Author string    `json:"author,omitempty"`
	At     time.Time `json:"at"`
}

// Related is one linked request as a thread shows it. Like GitHub, a thread
// lists the requests that mention it (with where), not the ones it mentions:
// those are visible, and clickable, in its own text.
type Related struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Kind is related, duplicate or mention.
	Kind string `json:"kind"`
	// Incoming: the other request links to this one (a backlink).
	Incoming bool `json:"incoming,omitempty"`
	// Mentions are where an incoming mention was written, oldest first.
	Mentions []Mention `json:"mentions,omitempty"`
}

// Mention is one place a request was mentioned: a message of another
// request (Message 0 is that request's title).
type Mention struct {
	Message int       `json:"message"`
	Author  string    `json:"author"`
	Agent   string    `json:"agent,omitempty"`
	At      time.Time `json:"at"`
	// Excerpt is the line holding the reference, shortened around it.
	Excerpt string `json:"excerpt"`
}

// mentionPattern matches a reference to a request of the queue with this key:
// KEY-n, or the shorthand KEY-n/m/k for several requests (THI-117).
func mentionPattern(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(key) + `-([0-9]{1,7}(?:/[0-9]{1,7})*)\b`)
}

// mentions lists the request numbers a text refers to as KEY-n (or KEY-n/m),
// each with the byte offset of the first digit of its first reference's KEY-n.
func mentions(pattern *regexp.Regexp, text string) [][2]int {
	seen := map[int]bool{}
	out := [][2]int{}
	for _, m := range pattern.FindAllStringSubmatchIndex(text, -1) {
		for _, part := range strings.Split(text[m[2]:m[3]], "/") {
			if n, err := strconv.Atoi(part); err == nil && n > 0 && !seen[n] {
				seen[n] = true
				out = append(out, [2]int{n, m[2]})
			}
		}
	}
	return out
}

const excerptRunes = 140

// excerpt is the line of text around offset, without list or quote markers,
// cut to excerptRunes around the reference.
func excerpt(text string, offset int) string {
	start := strings.LastIndexByte(text[:offset], '\n') + 1
	end := len(text)
	if i := strings.IndexByte(text[offset:], '\n'); i >= 0 {
		end = offset + i
	}
	before := []rune(text[start:offset])
	after := []rune(text[offset:end])
	cut := false
	if len(before) > excerptRunes/3 {
		before, cut = before[len(before)-excerptRunes/3:], true
	}
	line := string(before)
	if !cut {
		line = strings.TrimLeft(line, " \t-*>#0123456789.")
	}
	rest := excerptRunes - len([]rune(line))
	tail := ""
	if len(after) > rest {
		after, tail = after[:rest], "…"
	}
	line = strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(line + string(after)))
	if cut {
		line = "…" + line
	}
	return line + tail
}

// edge is one link between two requests; mention edges carry where the
// mentions were written.
type edge struct {
	From, To int
	Kind     string
	Sources  []Mention
}

// edges lists every link among a queue's requests: explicit ones, then
// mentions that no explicit link already covers. Links to requests that do
// not exist are dropped.
func edges(p Project, requests map[int]Request) []edge {
	out := []edge{}
	explicit := map[[2]int]bool{}
	for _, n := range sortedNumbers(requests) {
		for _, l := range requests[n].Links {
			if _, ok := requests[l.Number]; ok && l.Number != n {
				out = append(out, edge{From: n, To: l.Number, Kind: l.Kind})
				explicit[[2]int{n, l.Number}] = true
			}
		}
	}
	pattern := mentionPattern(p.Key)
	for _, n := range sortedNumbers(requests) {
		r := requests[n]
		texts := []Message{{ID: 0, Author: r.Author, Body: r.Title, At: r.Created}}
		texts = append(texts, r.Messages...)
		index := map[int]int{}
		for _, m := range texts {
			for _, found := range mentions(pattern, m.Body) {
				to := found[0]
				if _, ok := requests[to]; !ok || to == n || explicit[[2]int{n, to}] {
					continue
				}
				source := Mention{Message: m.ID, Author: m.Author, Agent: m.Agent, At: m.At, Excerpt: excerpt(m.Body, found[1]-len(p.Key)-1)}
				if i, ok := index[to]; ok {
					out[i].Sources = append(out[i].Sources, source)
					continue
				}
				index[to] = len(out)
				out = append(out, edge{From: n, To: to, Kind: "mention", Sources: []Mention{source}})
			}
		}
	}
	return out
}

func sortedNumbers(requests map[int]Request) []int {
	numbers := make([]int, 0, len(requests))
	for n := range requests {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	return numbers
}

// relatedFor is what request n's thread shows: its explicit links, then
// every request linking to or mentioning it, one row per other request and
// direction. The requests n itself mentions are links in its own text.
func relatedFor(n int, requests map[int]Request, all []edge) []Related {
	out := []Related{}
	seen := map[[2]int]bool{}
	add := func(e edge, other int, incoming bool) {
		dir := 0
		if incoming {
			dir = 1
		}
		if seen[[2]int{other, dir}] {
			return
		}
		seen[[2]int{other, dir}] = true
		r := requests[other]
		out = append(out, Related{Number: other, Title: r.Title, Status: r.Status, Kind: e.Kind, Incoming: incoming, Mentions: e.Sources})
	}
	for _, e := range all {
		if e.From == n && e.Kind != "mention" {
			add(e, e.To, false)
		}
	}
	for _, e := range all {
		if e.To == n {
			add(e, e.From, true)
		}
	}
	return out
}

func (s Store) projectRequests(project string) (map[int]Request, error) {
	if _, err := s.Project(project); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.projectDir(project), "requests"))
	if errors.Is(err, os.ErrNotExist) {
		return map[int]Request{}, nil
	} else if err != nil {
		return nil, err
	}
	requests := map[int]Request{}
	for _, entry := range entries {
		n, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if r, err := s.Request(project, n); err == nil {
			requests[r.Number] = r
		}
	}
	return requests, nil
}

// Related lists request n's links and backlinks.
func (s Store) Related(project string, n int) ([]Related, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	requests, err := s.projectRequests(project)
	if err != nil {
		return nil, err
	}
	if _, ok := requests[n]; !ok {
		return nil, ErrNotFound
	}
	return relatedFor(n, requests, edges(p, requests)), nil
}

// RefNumber reads a reference to a request of p: "THI-42", "thi-42", "#42"
// or "42".
func RefNumber(p Project, ref string) (int, bool) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	if dash := strings.LastIndexByte(ref, '-'); dash >= 0 {
		if !strings.EqualFold(ref[:dash], p.Key) {
			return 0, false
		}
		ref = ref[dash+1:]
	}
	n, err := strconv.Atoi(ref)
	return n, err == nil && n > 0
}

// SetLink adds, changes or (with kind "") removes request n's explicit link
// to request to. A request is a duplicate of at most one other: marking it
// a duplicate again moves that link. When agent is set, only the run working
// on n (session and agent) may change its links.
func (s Store) SetLink(project string, n, to int, kind, author, session, agent string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if kind != "" && kind != LinkRelated && kind != LinkDuplicate || to == n || !validText(author, 80) {
		return r, ErrInvalid
	}
	if _, err := s.Request(project, to); err != nil {
		return r, err
	}
	if agent != "" && (session == "" || r.Session != session || r.Agent != agent) {
		return r, ErrConflict
	}
	links := []Link{}
	for _, l := range r.Links {
		if l.Number == to || kind == LinkDuplicate && l.Kind == LinkDuplicate {
			continue
		}
		links = append(links, l)
	}
	if kind != "" {
		links = append(links, Link{Number: to, Kind: kind, Author: author, At: s.now()})
	}
	if len(links) == 0 {
		links = nil
	}
	r.Links = links
	return r, s.commit(s.requestPath(project, n), r)
}

// DuplicateOf is the request n was merged into, or 0.
func (r Request) DuplicateOf() int {
	for _, l := range r.Links {
		if l.Kind == LinkDuplicate {
			return l.Number
		}
	}
	return 0
}
