package board

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Every queue's threads are mirrored as markdown under
// ~/.fen/board/projects/<id>/threads/ (THI-103), so agents can search past
// requests with rg, grep or any other tool and link or merge what they find:
//
//	threads/INDEX.md   one line per request: reference, status, tag, title
//	threads/THI-42.md  front matter (status, tag, links, backlinks), then the thread
//
// The request JSON stays the source of truth. The mirror is derived: it is
// rewritten after board changes (materialize), files are written only when
// their content changed, and it is never read back. Deleting the folder is
// safe; the next board write recreates it.

func (s Store) threadsDir(project string) string {
	return filepath.Join(s.projectDir(project), "threads")
}

// ThreadsPath is where a project's threads are mirrored.
func (s Store) ThreadsPath(project string) string { return s.threadsDir(project) }

// MirrorThreads brings a project's thread mirror up to date.
func (s Store) MirrorThreads(project string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	requests, err := s.projectRequests(project)
	if err != nil {
		return err
	}
	all := edges(p, requests)
	dir := s.threadsDir(project)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	keep := map[string]bool{"INDEX.md": true}
	var index strings.Builder
	fmt.Fprintf(&index, "# %s threads\n\nOne line per request, newest first: reference, status, tag, title. Each thread is in <reference>.md here.\n\n", p.Name)
	numbers := sortedNumbers(requests)
	for i := len(numbers) - 1; i >= 0; i-- {
		r := requests[numbers[i]]
		name := fmt.Sprintf("%s-%d.md", p.Key, r.Number)
		keep[name] = true
		fmt.Fprintf(&index, "- %s-%d [%s] [%s] %s\n", p.Key, r.Number, r.Status, strings.Join(r.TagList(), ","), r.Title)
		if err := writeIfChanged(filepath.Join(dir, name), []byte(threadMarkdown(p, r, relatedFor(r.Number, requests, all)))); err != nil {
			return err
		}
	}
	if err := writeIfChanged(filepath.Join(dir, "INDEX.md"), []byte(index.String())); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !keep[entry.Name()] && strings.HasSuffix(entry.Name(), ".md") {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

// threadMarkdown is one request as a mirror file.
func threadMarkdown(p Project, r Request, related []Related) string {
	ref := func(n int) string { return fmt.Sprintf("%s-%d", p.Key, n) }
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", ref(r.Number))
	fmt.Fprintf(&b, "queue: %s\n", p.ID)
	fmt.Fprintf(&b, "title: %s\n", strconv.Quote(r.Title))
	fmt.Fprintf(&b, "status: %s\n", r.Status)
	fmt.Fprintf(&b, "tag: %s\n", r.Tag)
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(r.TagList(), ", "))
	fmt.Fprintf(&b, "author: %s\n", strconv.Quote(r.Author))
	if r.Agent != "" {
		fmt.Fprintf(&b, "agent: %s\n", r.Agent)
	}
	fmt.Fprintf(&b, "created: %s\n", r.Created.UTC().Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&b, "updated: %s\n", r.Updated.UTC().Format("2006-01-02T15:04:05Z"))
	if d := r.DuplicateOf(); d != 0 {
		fmt.Fprintf(&b, "duplicate_of: %s\n", ref(d))
	}
	var links, backlinks []string
	for _, rel := range related {
		item := fmt.Sprintf("%s (%s)", ref(rel.Number), rel.Kind)
		if rel.Incoming {
			backlinks = append(backlinks, item)
		} else {
			links = append(links, item)
		}
	}
	if len(links) > 0 {
		fmt.Fprintf(&b, "links: [%s]\n", strings.Join(links, ", "))
	}
	if len(backlinks) > 0 {
		fmt.Fprintf(&b, "backlinks: [%s]\n", strings.Join(backlinks, ", "))
	}
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s: %s\n", ref(r.Number), r.Title)
	if len(related) > 0 {
		b.WriteString("\n## Linked requests\n\n")
		for _, rel := range related {
			verb := map[string]string{LinkRelated: "related to", LinkDuplicate: "duplicate of", "mention": "mentions"}[rel.Kind]
			if rel.Incoming {
				verb = map[string]string{LinkRelated: "related to", LinkDuplicate: "has duplicate", "mention": "mentioned by"}[rel.Kind]
			}
			fmt.Fprintf(&b, "- %s %s [%s] %s\n", verb, ref(rel.Number), rel.Status, rel.Title)
			for _, m := range rel.Mentions {
				who := m.Author
				if m.Agent != "" {
					who = m.Agent + " (agent)"
				}
				fmt.Fprintf(&b, "  - %s, %s: %s\n", who, m.At.UTC().Format("2006-01-02 15:04 UTC"), m.Excerpt)
			}
		}
	}
	for _, m := range r.Messages {
		who := m.Author
		if m.Agent != "" {
			who = m.Agent + " (agent)"
		}
		fmt.Fprintf(&b, "\n## %s, %s\n\n%s\n", who, m.At.UTC().Format("2006-01-02 15:04 UTC"), strings.TrimRight(m.Body, "\n"))
		for _, a := range m.Attachments {
			fmt.Fprintf(&b, "\n[image: %s]\n", a.Name)
		}
	}
	return b.String()
}

// writeIfChanged replaces path with data unless it already holds it. The
// mirror is derived and rebuilt on demand, so it skips fsync.
func writeIfChanged(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// mirrorThreads runs after materialize: it refreshes the mirror of every
// project whose requests changed, and of any project not mirrored yet. A
// failed mirror never fails a board write.
func (s Store) mirrorThreads(objects map[string]map[string]stampedField, changed map[string]bool) {
	for object := range objects {
		if parts := strings.Split(object, "/"); len(parts) == 3 && parts[2] == "project.json" {
			if _, err := os.Stat(s.threadsDir(parts[1])); errors.Is(err, os.ErrNotExist) {
				changed[parts[1]] = true
			}
		}
	}
	for project := range changed {
		_ = s.MirrorThreads(project)
	}
}
