package board

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A Concept is one OKF knowledge file: YAML front matter, then markdown.
// See https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md
// Agents write them; people review them in Fennec.
type Concept struct {
	// Path inside the bundle without ".md", such as "runbooks/attach-eof".
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags"`
	Sources     []string `json:"sources"`
	Status      string   `json:"status"`
	GeneratedBy string   `json:"generated_by,omitempty"`
	GeneratedAt string   `json:"generated_at,omitempty"`
	Verified    []Actor  `json:"verified"`
	Body        string   `json:"body"`
}

type Actor struct {
	By string `json:"by"`
	At string `json:"at,omitempty"`
}

var conceptID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}(?:/[a-z0-9][a-z0-9_-]{0,63}){0,3}$`)

const maxConcept = 256 << 10

func (s Store) knowledgeDir(project string) string {
	return filepath.Join(s.projectDir(project), "knowledge")
}

// KnowledgePath is where agents write a project's concepts.
func (s Store) KnowledgePath(project string) string { return s.knowledgeDir(project) }

func (s Store) Knowledge(project string) ([]Concept, error) {
	if _, err := s.Project(project); err != nil {
		return nil, err
	}
	root := s.knowledgeDir(project)
	concepts := []Concept{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		id := filepath.ToSlash(strings.TrimSuffix(relative, ".md"))
		if !conceptID.MatchString(id) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxConcept {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if c, ok := parseConcept(id, string(data)); ok {
			concepts = append(concepts, c)
		}
		return nil
	})
	sort.Slice(concepts, func(i, j int) bool { return concepts[i].ID < concepts[j].ID })
	return concepts, err
}

// Review marks a concept reviewed by a person (Draft becomes Current), or
// deprecates or restores it. Only the status and verified fields change; the
// rest of the file is kept as the agent wrote it.
func (s Store) Review(project, id, action, person string) (Concept, error) {
	unlock, err := s.lock()
	if err != nil {
		return Concept{}, err
	}
	defer unlock()
	if _, err := s.Project(project); err != nil {
		return Concept{}, err
	}
	if !conceptID.MatchString(id) || !validText(person, 80) || strings.ContainsAny(person, "\r\n:") {
		return Concept{}, ErrInvalid
	}
	path := filepath.Join(s.knowledgeDir(project), filepath.FromSlash(id)+".md")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Concept{}, ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConcept {
		return Concept{}, ErrInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Concept{}, err
	}
	front, body, ok := splitFrontMatter(string(data))
	if !ok {
		return Concept{}, ErrInvalid
	}
	entries := topLevel(front)
	c, _ := parseConcept(id, string(data))
	switch action {
	case "review":
		c.Verified = append(c.Verified, Actor{By: "human:" + person, At: s.now().Format(time.RFC3339)})
		entries = setEntry(entries, "verified", renderActors(c.Verified))
		if c.Status == "draft" || c.Status == "" {
			c.Status = "stable"
			entries = setEntry(entries, "status", []string{"status: stable"})
		}
	case "deprecate":
		c.Status = "deprecated"
		entries = setEntry(entries, "status", []string{"status: deprecated"})
	case "restore":
		c.Status = "stable"
		entries = setEntry(entries, "status", []string{"status: stable"})
	default:
		return Concept{}, ErrInvalid
	}
	var out strings.Builder
	out.WriteString("---\n")
	for _, e := range entries {
		for _, line := range e.lines {
			out.WriteString(line + "\n")
		}
	}
	out.WriteString("---\n" + body)
	if err = writeFile(path, []byte(out.String())); err != nil {
		return Concept{}, err
	}
	return c, nil
}

func writeFile(path string, data []byte) error {
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

func renderActors(actors []Actor) []string {
	lines := []string{"verified:"}
	for _, a := range actors {
		lines = append(lines, "  - by: "+quote(a.By))
		if a.At != "" {
			lines = append(lines, "    at: "+quote(a.At))
		}
	}
	return lines
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, ":#'\"[]{},&*!|>%@`") && strings.TrimSpace(s) == s {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func splitFrontMatter(text string) (string, string, bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", false
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", false
	}
	body := rest[end+4:]
	body = strings.TrimPrefix(body, "\n")
	return rest[:end], body, true
}

type entry struct {
	key   string
	lines []string
}

// topLevel splits front matter into top-level keys with their lines,
// including nested and continuation lines.
func topLevel(front string) []entry {
	var entries []entry
	for _, line := range strings.Split(front, "\n") {
		if line != "" && line[0] != ' ' && line[0] != '\t' && line[0] != '-' && line[0] != '#' {
			key, _, _ := strings.Cut(line, ":")
			entries = append(entries, entry{key: strings.TrimSpace(key), lines: []string{line}})
			continue
		}
		if len(entries) == 0 {
			entries = append(entries, entry{})
		}
		entries[len(entries)-1].lines = append(entries[len(entries)-1].lines, line)
	}
	return entries
}

func setEntry(entries []entry, key string, lines []string) []entry {
	for i, e := range entries {
		if e.key == key {
			entries[i].lines = lines
			return entries
		}
	}
	return append(entries, entry{key: key, lines: lines})
}

// parseConcept reads the front matter fields Fennec shows. It understands the
// block and flow forms of YAML that OKF files use: scalars, lists of scalars,
// and maps or lists of maps one level deep.
func parseConcept(id, text string) (Concept, bool) {
	front, body, ok := splitFrontMatter(text)
	if !ok {
		return Concept{}, false
	}
	c := Concept{ID: id, Body: body, Tags: []string{}, Sources: []string{}, Verified: []Actor{}}
	for _, e := range topLevel(front) {
		_, value, _ := strings.Cut(e.lines[0], ":")
		value = strings.TrimSpace(value)
		nested := e.lines[1:]
		switch e.key {
		case "type":
			c.Type = scalar(value)
		case "title":
			c.Title = scalar(value)
		case "description":
			c.Description = scalar(value)
		case "status":
			c.Status = scalar(value)
		case "tags":
			c.Tags = list(value, nested)
		case "sources":
			c.Sources = list(value, nested)
		case "generated":
			m := mapping(value, nested)
			c.GeneratedBy, c.GeneratedAt = m["by"], m["at"]
		case "verified":
			for _, m := range mappings(value, nested) {
				if m["by"] != "" {
					c.Verified = append(c.Verified, Actor{By: m["by"], At: m["at"]})
				}
			}
		}
	}
	if c.Type == "" {
		return c, false
	}
	if c.Title == "" {
		c.Title = id[strings.LastIndexByte(id, '/')+1:]
	}
	return c, true
}

func scalar(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"') {
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func flowItems(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	var items []string
	for _, part := range strings.Split(v, ",") {
		if s := scalar(part); s != "" {
			items = append(items, s)
		}
	}
	return items
}

func list(value string, nested []string) []string {
	items := []string{}
	if strings.HasPrefix(value, "[") {
		return append(items, flowItems(value)...)
	}
	for _, line := range nested {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") {
			items = append(items, scalar(t[2:]))
		}
	}
	return items
}

func flowMap(v string) map[string]string {
	m := map[string]string{}
	v = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "{"), "}")
	for _, part := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(part, ":")
		if ok {
			m[strings.TrimSpace(k)] = scalar(val)
		}
	}
	return m
}

func mapping(value string, nested []string) map[string]string {
	if strings.HasPrefix(value, "{") {
		return flowMap(value)
	}
	m := map[string]string{}
	for _, line := range nested {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			m[strings.TrimSpace(k)] = scalar(v)
		}
	}
	return m
}

func mappings(value string, nested []string) []map[string]string {
	var out []map[string]string
	if strings.HasPrefix(value, "[") {
		inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "["), "]")
		for _, part := range strings.Split(inner, "},") {
			out = append(out, flowMap(strings.TrimSpace(part)+"}"))
		}
		return out
	}
	for _, line := range nested {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") {
			t = strings.TrimSpace(t[2:])
			if strings.HasPrefix(t, "{") {
				out = append(out, flowMap(t))
				continue
			}
			out = append(out, map[string]string{})
		}
		if len(out) == 0 {
			continue
		}
		if k, v, ok := strings.Cut(t, ":"); ok {
			out[len(out)-1][strings.TrimSpace(k)] = scalar(v)
		}
	}
	return out
}
