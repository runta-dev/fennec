package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"fennec/cua/internal/protocol"
)

// Limits of a spec file.
const (
	MaxDescription = 2048
	MaxServiceName = 32
	MaxToolName    = 64
)

// ReservedPrefixes start no service tool name: they belong to fennec-cua.
var ReservedPrefixes = []string{"cua_", "desk_", "model_", "safety_"}

var (
	pluginName  = regexp.MustCompile(`^[a-z][a-z0-9_]*_run$`)
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Plugin is a script plugin: a `*_run` tool that runs its own program under
// the run tool's contract.
type Plugin struct {
	Name        string
	Description string
	Entry       string // absolute program path
	Resources   []Resource
	File        string // the spec file it came from
}

// Resource is a file a plugin serves through resources/read.
type Resource struct {
	URI         string
	Path        string
	MIME        string
	Description string
}

// Service is a long-lived service plugin.
type Service struct {
	Name    string
	Program string // absolute
	Args    []string
	Tools   []ServiceTool
	File    string
}

// ServiceTool is one tool a service answers.
type ServiceTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	// MCP lists the tool over MCP; false hides it and refuses its calls.
	MCP bool
}

// Set is what a plugin directory declares, in file-name order.
type Set struct {
	Plugins  []Plugin
	Services []Service
}

// Dir is the plugin directory: configured (relative to the configuration
// file's directory) when set, else tools.d beside the configuration file.
func Dir(configPath, configured string) string {
	base := filepath.Dir(configPath)
	if configured == "" {
		return filepath.Join(base, "tools.d")
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(base, configured)
}

// The spec files' shapes; unknown fields are refused.
type pluginFile struct {
	Name        *string        `json:"name"`
	Description *string        `json:"description"`
	Entry       *string        `json:"entry"`
	Resources   []resourceFile `json:"resources"`
}

type resourceFile struct {
	URI         string `json:"uri"`
	Path        string `json:"path"`
	MIME        string `json:"mime"`
	Description string `json:"description"`
}

type serviceFile struct {
	Service *string    `json:"service"`
	Program *string    `json:"program"`
	Args    []string   `json:"args"`
	Tools   []toolFile `json:"tools"`
}

type toolFile struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	MCP         *bool           `json:"mcp"`
}

// Load reads every *.json file of dir in file-name order. A file that breaks
// any rule is skipped whole with one log line naming it and the reason; a
// missing directory loads nothing.
func Load(dir string, logf func(format string, args ...any)) Set {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("plugins: cannot read %s: %v", dir, err)
		}
		return Set{}
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	l := &loader{taken: map[string]bool{}, uris: map[string]bool{}, services: map[string]bool{}}
	for _, n := range protocol.Tools {
		l.taken[n] = true
	}
	// The REPL's tools and the internal tools it calls are reserved too.
	for _, n := range []string{protocol.ToolJS, protocol.ToolJSReset, protocol.ToolTurnEnded, protocol.ToolApps, protocol.ToolPaste, "extensions"} {
		l.taken[n] = true
	}
	for _, n := range names {
		path := filepath.Join(dir, n)
		data, err := os.ReadFile(path)
		if err == nil {
			err = l.add(path, data)
		}
		if err != nil {
			logf("plugins: skipped %s: %v", path, err)
		}
	}
	return l.set
}

type loader struct {
	set      Set
	taken    map[string]bool // tool names
	uris     map[string]bool
	services map[string]bool
}

func strict(data []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("not a valid spec: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("not a valid spec: data after the object")
	}
	return nil
}

func (l *loader) add(path string, data []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("not a JSON object: %v", err)
	}
	if _, ok := keys["service"]; ok {
		s, err := l.service(data)
		if err != nil {
			return err
		}
		s.File = path
		l.set.Services = append(l.set.Services, s)
		l.services[s.Name] = true
		for _, t := range s.Tools {
			l.taken[t.Name] = true
		}
		return nil
	}
	p, err := l.plugin(data)
	if err != nil {
		return err
	}
	p.File = path
	l.set.Plugins = append(l.set.Plugins, p)
	l.taken[p.Name] = true
	for _, r := range p.Resources {
		l.uris[r.URI] = true
	}
	return nil
}

func description(d string) error {
	if utf8.RuneCountInString(d) > MaxDescription {
		return fmt.Errorf("description is longer than %d characters", MaxDescription)
	}
	return nil
}

func (l *loader) plugin(data []byte) (Plugin, error) {
	var f pluginFile
	if err := strict(data, &f); err != nil {
		return Plugin{}, err
	}
	switch {
	case f.Name == nil:
		return Plugin{}, errors.New("name is missing")
	case !pluginName.MatchString(*f.Name):
		return Plugin{}, fmt.Errorf("name %q must be lowercase letters, digits and underscores, start with a letter and end in _run", *f.Name)
	case l.taken[*f.Name]:
		return Plugin{}, fmt.Errorf("name %q is built in or already taken", *f.Name)
	case f.Description == nil:
		return Plugin{}, errors.New("description is missing")
	case f.Entry == nil || !filepath.IsAbs(*f.Entry):
		return Plugin{}, errors.New("entry must be an absolute path")
	}
	if err := description(*f.Description); err != nil {
		return Plugin{}, err
	}
	p := Plugin{Name: *f.Name, Description: *f.Description, Entry: *f.Entry}
	seen := map[string]bool{}
	for i, r := range f.Resources {
		switch {
		case r.URI == "" || r.MIME == "" || r.Description == "":
			return Plugin{}, fmt.Errorf("resource %d needs uri, path, mime and description", i)
		case !filepath.IsAbs(r.Path):
			return Plugin{}, fmt.Errorf("resource %s: path must be absolute", r.URI)
		case seen[r.URI] || l.uris[r.URI]:
			return Plugin{}, fmt.Errorf("resource uri %s is already taken", r.URI)
		}
		seen[r.URI] = true
		p.Resources = append(p.Resources, Resource(r))
	}
	return p, nil
}

func (l *loader) service(data []byte) (Service, error) {
	var f serviceFile
	if err := strict(data, &f); err != nil {
		return Service{}, err
	}
	switch {
	case f.Service == nil || !serviceName.MatchString(*f.Service) || len(*f.Service) > MaxServiceName:
		return Service{}, fmt.Errorf("service must be lowercase letters, digits and underscores, start with a letter and be at most %d characters", MaxServiceName)
	case l.services[*f.Service]:
		return Service{}, fmt.Errorf("service %q is already declared", *f.Service)
	case f.Program == nil || !filepath.IsAbs(*f.Program):
		return Service{}, errors.New("program must be an absolute path")
	case len(f.Tools) == 0:
		return Service{}, errors.New("a service needs at least one tool")
	}
	s := Service{Name: *f.Service, Program: *f.Program, Args: f.Args}
	mine := map[string]bool{}
	for _, t := range f.Tools {
		if err := l.serviceTool(t, mine); err != nil {
			return Service{}, err
		}
		mine[t.Name] = true
		st := ServiceTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, MCP: true}
		if t.MCP != nil {
			st.MCP = *t.MCP
		}
		s.Tools = append(s.Tools, st)
	}
	return s, nil
}

func (l *loader) serviceTool(t toolFile, mine map[string]bool) error {
	if !serviceName.MatchString(t.Name) || len(t.Name) > MaxToolName {
		return fmt.Errorf("tool name %q must be lowercase letters, digits and underscores, start with a letter and be at most %d characters", t.Name, MaxToolName)
	}
	if strings.HasSuffix(t.Name, "_run") {
		return fmt.Errorf("tool name %q ends in _run, which script plugins take", t.Name)
	}
	for _, p := range ReservedPrefixes {
		if strings.HasPrefix(t.Name, p) {
			return fmt.Errorf("tool name %q starts with the reserved prefix %s", t.Name, p)
		}
	}
	if l.taken[t.Name] || mine[t.Name] {
		return fmt.Errorf("tool name %q is built in or already taken", t.Name)
	}
	if err := description(t.Description); err != nil {
		return fmt.Errorf("tool %s: %v", t.Name, err)
	}
	var schema struct {
		Type string `json:"type"`
	}
	if len(t.InputSchema) == 0 || json.Unmarshal(t.InputSchema, &schema) != nil || schema.Type != "object" {
		return fmt.Errorf("tool %s: input_schema must be an object schema (type object)", t.Name)
	}
	return nil
}
