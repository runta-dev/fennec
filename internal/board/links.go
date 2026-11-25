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

// Related is one link as a thread shows it.
type Related struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Kind is related, duplicate or mention.
	Kind string `json:"kind"`
	// Incoming: the other request links to this one (a backlink).
	Incoming bool `json:"incoming,omitempty"`
}

// mentionPattern matches a reference to a request of the queue with this key.
func mentionPattern(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(key) + `-([0-9]{1,7})\b`)
}

// mentions lists the request numbers a text refers to as KEY-n.
func mentions(pattern *regexp.Regexp, text string) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// edge is one link between two requests; mention edges come from messages.
type edge struct {
	From, To int
	Kind     string
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
				out = append(out, edge{n, l.Number, l.Kind})
				explicit[[2]int{n, l.Number}] = true
			}
		}
	}
	pattern := mentionPattern(p.Key)
	for _, n := range sortedNumbers(requests) {
		r := requests[n]
		seen := map[int]bool{}
		texts := []string{r.Title}
		for _, m := range r.Messages {
			texts = append(texts, m.Body)
		}
		for _, text := range texts {
			for _, to := range mentions(pattern, text) {
				if _, ok := requests[to]; !ok || to == n || seen[to] || explicit[[2]int{n, to}] {
					continue
				}
				seen[to] = true
				out = append(out, edge{n, to, "mention"})
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

// relatedFor is what request n's thread shows: its links, then its
// backlinks, one row per other request and direction.
func relatedFor(n int, requests map[int]Request, all []edge) []Related {
	out := []Related{}
	seen := map[[2]int]bool{}
	add := func(other int, kind string, incoming bool) {
		dir := 0
		if incoming {
			dir = 1
		}
		if seen[[2]int{other, dir}] {
			return
		}
		seen[[2]int{other, dir}] = true
		r := requests[other]
		out = append(out, Related{Number: other, Title: r.Title, Status: r.Status, Kind: kind, Incoming: incoming})
	}
	for _, e := range all {
		if e.From == n {
			add(e.To, e.Kind, false)
		}
	}
	for _, e := range all {
		if e.To == n {
			add(e.From, e.Kind, true)
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
