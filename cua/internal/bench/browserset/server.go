package browserset

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry kinds in the server's log.
const (
	KindPage     = "page"     // an HTML page was served
	KindSubmit   = "submit"   // a form was posted
	KindDownload = "download" // a file was served as an attachment
	KindEvent    = "event"    // a page reported something (/event/NAME)
)

// Entry is one line of the server's log.
type Entry struct {
	Seq    int                 `json:"seq"`
	Time   time.Time           `json:"time"`
	Kind   string              `json:"kind"`
	Path   string              `json:"path"`
	Query  string              `json:"query,omitempty"`
	Fields map[string][]string `json:"fields,omitempty"`
}

// LogName is the log file in the log folder.
const LogName = "log.jsonl"

// Log appends entries to LogName, one JSON object per line, synced after
// each so a grader reading it right after an action sees it.
type Log struct {
	mu  sync.Mutex
	f   *os.File
	seq int
}

// OpenLog opens (creating it) the log in dir.
func OpenLog(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	old, err := ReadLog(dir)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, LogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f}
	if n := len(old); n > 0 {
		l.seq = old[n-1].Seq
	}
	return l, nil
}

// Add appends an entry.
func (l *Log) Add(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := l.f.Write(append(data, '\n')); err != nil {
		return err
	}
	return l.f.Sync()
}

// Last is the sequence number of the latest entry.
func (l *Log) Last() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Close closes the file.
func (l *Log) Close() error { return l.f.Close() }

// ReadLog reads every entry in dir's log; a missing log is empty.
func ReadLog(dir string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(dir, LogName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<22)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s: %w", LogName, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Server serves the site and logs what it sees.
type Server struct {
	site      fs.FS
	log       *Log
	shipments *template.Template
	invoices  *template.Template
}

// NewServer serves site (the embedded site/ folder) and records to log.
func NewServer(site fs.FS, log *Log) (*Server, error) {
	s := &Server{site: site, log: log}
	var err error
	if s.shipments, err = template.ParseFS(site, "larkspur/shipments.tmpl"); err != nil {
		return nil, err
	}
	if s.invoices, err = template.ParseFS(site, "copperleaf/invoices.tmpl"); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) record(e Entry) {
	if err := s.log.Add(e); err != nil {
		fmt.Fprintln(os.Stderr, "bench-pages: log:", err)
	}
}

// ServeHTTP answers one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := path.Clean("/" + r.URL.Path)
	if strings.HasSuffix(r.URL.Path, "/") && p != "/" {
		p += "/"
	}
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case p == "/healthz":
		_, _ = w.Write([]byte("ok\n"))
		return
	case strings.HasPrefix(p, "/event/"):
		s.record(Entry{Kind: KindEvent, Path: p})
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodPost:
		s.submit(w, r, p)
		return
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case strings.HasPrefix(p, "/marrowby/files/"):
		name := strings.TrimPrefix(p, "/marrowby/files/")
		data, ok := Report(name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.record(Entry{Kind: KindDownload, Path: p})
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	case p == "/larkspur/shipments":
		s.renderShipments(w, r)
	case p == "/copperleaf/invoices":
		s.page(w, r, p, func(w http.ResponseWriter) error { return s.invoices.Execute(w, Invoices()) })
	default:
		s.static(w, r, p)
	}
}

// page records an HTML page and renders it.
func (s *Server) page(w http.ResponseWriter, r *http.Request, p string, render func(http.ResponseWriter) error) {
	s.record(Entry{Kind: KindPage, Path: p, Query: r.URL.RawQuery})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := render(w); err != nil {
		fmt.Fprintln(os.Stderr, "bench-pages: render:", err)
	}
}

type pageLink struct {
	N       int
	Current bool
}

func (s *Server) renderShipments(w http.ResponseWriter, r *http.Request) {
	all := Shipments()
	pages := (len(all) + ShipmentsPerPage - 1) / ShipmentsPerPage
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		n = 1
	}
	n = min(n, pages)
	var links []pageLink
	for i := 1; i <= pages; i++ {
		links = append(links, pageLink{N: i, Current: i == n})
	}
	data := map[string]any{
		"Page": n, "Pages": pages, "Prev": n - 1, "Next": n + 1, "Total": len(all), "Links": links,
		"Rows": all[(n-1)*ShipmentsPerPage : min(n*ShipmentsPerPage, len(all))],
	}
	s.page(w, r, "/larkspur/shipments", func(w http.ResponseWriter) error { return s.shipments.Execute(w, data) })
}

// static serves a file of the site: "/x/" is x/index.html, "/x/y" is
// x/y.html when it exists, else the file itself.
func (s *Server) static(w http.ResponseWriter, r *http.Request, p string) {
	rel := strings.TrimPrefix(p, "/")
	var candidates []string
	switch {
	case rel == "":
		candidates = []string{"start/index.html"}
	case strings.HasSuffix(rel, "/"):
		candidates = []string{rel + "index.html"}
	default:
		candidates = []string{rel + ".html", rel}
	}
	for _, c := range candidates {
		if strings.HasSuffix(c, ".tmpl") {
			continue
		}
		data, err := fs.ReadFile(s.site, c)
		if err != nil {
			continue
		}
		ctype := "application/octet-stream"
		switch path.Ext(c) {
		case ".html":
			ctype = "text/html; charset=utf-8"
			s.record(Entry{Kind: KindPage, Path: p, Query: r.URL.RawQuery})
		case ".css":
			ctype = "text/css; charset=utf-8"
		case ".js":
			ctype = "text/javascript; charset=utf-8"
		}
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write(data)
		return
	}
	http.NotFound(w, r)
}

var thanks = template.Must(template.New("thanks").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Received</title><link rel="stylesheet" href="/static/site.css"></head>
<body><main><div class="card"><h1>Thank you</h1><p>Your submission was received.</p>
<p class="muted small">Reference {{.}}</p></div></main></body></html>
`))

func (s *Server) submit(w http.ResponseWriter, r *http.Request, p string) {
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		err = r.ParseMultipartForm(1 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	fields := map[string][]string{}
	for k, v := range r.PostForm {
		fields[k] = v
	}
	s.record(Entry{Kind: KindSubmit, Path: p, Fields: fields})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = thanks.Execute(w, fmt.Sprintf("S-%d", s.log.Last()))
}
