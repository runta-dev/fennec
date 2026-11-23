package board

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Handler serves the board API. It has no listener of its own: it is reached
// only through the authenticated resident `board` socket (Dial). Do not expose
// it via ingress.
func (s Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sync", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		var in Exchange
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeError(w, ErrInvalid)
			return
		}
		out, err := s.Sync(in)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	decode := func(w http.ResponseWriter, r *http.Request, value any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, maxIcon+maxBody+(16<<10))
		if err := json.NewDecoder(r.Body).Decode(value); err != nil {
			writeError(w, ErrInvalid)
			return false
		}
		return true
	}
	number := func(w http.ResponseWriter, r *http.Request) (int, bool) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 1 {
			writeError(w, ErrNotFound)
			return 0, false
		}
		return n, true
	}
	reply := func(w http.ResponseWriter, value any, err error) {
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		projects, err := s.Projects()
		type row struct {
			Project
			SyncStatus string `json:"sync_status"`
			Open       int    `json:"open"`
			NeedsYou   int    `json:"needs_you"`
		}
		rows := []row{}
		for _, p := range projects {
			list, _ := s.Requests(p.ID)
			item := row{Project: p, SyncStatus: s.SyncStatus()}
			for _, sum := range list {
				if sum.Status != Resolved {
					item.Open++
				}
				if sum.Status == NeedsYou {
					item.NeedsYou++
				}
			}
			rows = append(rows, item)
		}
		reply(w, rows, err)
	})
	mux.HandleFunc("POST /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		var p Project
		if decode(w, r, &p) {
			p, err := s.CreateProject(p)
			reply(w, p, err)
		}
	})
	mux.HandleFunc("GET /v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Project(r.PathValue("id"))
		reply(w, p, err)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := s.DeleteProject(r.PathValue("id"))
		reply(w, map[string]bool{"deleted": err == nil}, err)
	})
	mux.HandleFunc("PATCH /v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		var change json.RawMessage
		if decode(w, r, &change) {
			p, err := s.UpdateProject(r.PathValue("id"), change)
			reply(w, p, err)
		}
	})
	mux.HandleFunc("GET /v1/projects/{id}/requests", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Requests(r.PathValue("id"))
		reply(w, list, err)
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Author, Title, Tag, Body string }
		if decode(w, r, &body) {
			req, err := s.CreateRequest(r.PathValue("id"), body.Author, body.Title, body.Tag, body.Body)
			reply(w, req, err)
		}
	})
	mux.HandleFunc("GET /v1/projects/{id}/requests/{n}", func(w http.ResponseWriter, r *http.Request) {
		if n, ok := number(w, r); ok {
			req, err := s.Request(r.PathValue("id"), n)
			reply(w, req, err)
		}
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/requests/{n}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Tag, Status string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			req, err := s.Change(r.PathValue("id"), n, body.Tag, body.Status)
			reply(w, req, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Author, Agent, Body string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			req, err := s.Reply(r.PathValue("id"), n, body.Author, body.Agent, body.Body)
			reply(w, req, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/claim", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Session, Agent, Location string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			if body.Location == "" {
				body.Location = "cloud"
			}
			req, err := s.ClaimAt(r.PathValue("id"), n, body.Session, body.Agent, body.Location)
			reply(w, req, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/continue", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Author, Body string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			value, err := s.Continue(r.PathValue("id"), n, body.Author, body.Body)
			reply(w, value, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/move", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Before int }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			value, err := s.Move(r.PathValue("id"), n, body.Before)
			reply(w, value, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/complete", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Session, Agent, Body, Outcome string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			value, err := s.Complete(r.PathValue("id"), n, body.Session, body.Agent, body.Body, body.Outcome)
			reply(w, value, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/fail", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Session string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			value, err := s.Fail(r.PathValue("id"), n, body.Session)
			reply(w, value, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Session, State string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			value, err := s.Reconcile(r.PathValue("id"), n, body.Session, body.State)
			reply(w, value, err)
		}
	})
	mux.HandleFunc("POST /v1/projects/{id}/requests/{n}/release", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Session string }
		if n, ok := number(w, r); ok && decode(w, r, &body) {
			req, err := s.Release(r.PathValue("id"), n, body.Session)
			reply(w, req, err)
		}
	})
	mux.HandleFunc("GET /v1/projects/{id}/knowledge", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Knowledge(r.PathValue("id"))
		reply(w, list, err)
	})
	mux.HandleFunc("POST /v1/projects/{id}/knowledge/review", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID, Action, Person string }
		if decode(w, r, &body) {
			c, err := s.Review(r.PathValue("id"), body.ID, body.Action, body.Person)
			reply(w, c, err)
		}
	})
	mux.HandleFunc("GET /v1/queue", func(w http.ResponseWriter, r *http.Request) {
		queue, err := s.Queue()
		reply(w, queue, err)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Only the resident socket's in-memory client calls this; refuse
		// anything that looks like a browser.
		if r.Header.Get("Origin") != "" || (r.Host != "board.fennec" && r.Host != "") {
			http.Error(w, "board access denied", http.StatusForbidden)
			return
		}
		if err := s.Recover(); err != nil {
			writeError(w, err)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, err error) {
	code, message := http.StatusInternalServerError, "board unavailable"
	switch {
	case errors.Is(err, ErrNotFound):
		code, message = http.StatusNotFound, "not found"
	case errors.Is(err, ErrInvalid):
		code, message = http.StatusBadRequest, "invalid"
	case errors.Is(err, ErrConflict):
		code, message = http.StatusConflict, "conflict"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Dial runs HTTP over an in-memory connection. There is no listening TCP port
// and closing or cancelling the resident stream tears its HTTP connection down.
func (s Store) Dial(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	listener := &oneConn{conn: server, done: make(chan struct{})}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	srv.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			listener.Close()
		}
	}
	stop := context.AfterFunc(ctx, func() { srv.Close(); server.Close(); client.Close(); listener.Close() })
	go func() { defer stop(); srv.Serve(listener) }()
	return client, nil
}

type oneConn struct {
	mu   sync.Mutex
	conn net.Conn
	done chan struct{}
	once sync.Once
}

func (l *oneConn) Accept() (net.Conn, error) {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	<-l.done
	return nil, io.EOF
}

func (l *oneConn) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *oneConn) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "board" }
