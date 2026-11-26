package board

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A Concept is one OKF knowledge file: YAML front matter, then markdown.
// See https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md
// Agents write and maintain them; Fennec only shows them.
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
	Body        string   `json:"body"`
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

// parseConcept reads the front matter fields Fennec shows. It understands the
// block and flow forms of YAML that OKF files use: scalars, lists of scalars,
// and maps one level deep.
func parseConcept(id, text string) (Concept, bool) {
	front, body, ok := splitFrontMatter(text)
	if !ok {
		return Concept{}, false
	}
	c := Concept{ID: id, Body: body, Tags: []string{}, Sources: []string{}}
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
		}
	}
	// OKF requires type, but agents have left it out (FEN-53: a whole bundle
	// without it showed as empty). Any file with front matter is listed.
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
