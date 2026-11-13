package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	benchpage "fennec/cua/bench"
	"fennec/cua/internal/bench"
)

type webFlags struct {
	results, ids, set, sets, model *string
	trials                         *int
}

func addWebFlags(fs *flag.FlagSet) webFlags {
	return webFlags{
		results: fs.String("results", "bench-results", "results directory"),
		ids:     fs.String("ids", "", "task id file to show instead of the evaluation sets"),
		set:     fs.String("set", "", "show only this evaluation set (default every set in -sets)"),
		sets:    fs.String("sets", "bench/sets", "directory of the set files"),
		model:   fs.String("model", "zai/glm-5.3-flashx", "the agent model the runs used"),
		trials:  fs.Int("trials", 3, "trials planned per task and backend"),
	}
}

func (f webFlags) lists() ([]bench.IDList, error) {
	switch {
	case *f.ids != "":
		l, err := bench.ReadIDList(*f.ids)
		return []bench.IDList{l}, err
	case *f.set != "":
		l, err := bench.ReadIDList(bench.SetPath(*f.sets, *f.set))
		if l.Name == "" {
			l.Name = *f.set
		}
		return []bench.IDList{l}, err
	}
	ls, err := bench.ReadSets(*f.sets)
	if err == nil && len(ls) == 0 {
		err = errors.New(*f.sets + ": no set files")
	}
	return ls, err
}

func (f webFlags) board() (bench.Board, error) {
	lists, err := f.lists()
	if err != nil {
		return bench.Board{}, err
	}
	scored, harness, err := bench.LoadResults(*f.results)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return bench.Board{}, err
	}
	return bench.NewBoard(lists, scored, harness, *f.trials, *f.model), nil
}

// sealedTasks are the tasks of sealed sets, whose runs the page never
// details.
func (f webFlags) sealedTasks() (map[string]bool, error) {
	lists, err := f.lists()
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, l := range lists {
		if l.Sealed {
			for _, id := range l.IDs {
				m[id] = true
			}
		}
	}
	return m, nil
}

// serveWeb serves the results page with data read fresh from the results
// directory on every request, plus each run's transcript and screenshot.
func serveWeb(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	f := addWebFlags(fs)
	addr := fs.String("addr", "127.0.0.1:8090", "listen address")
	page := fs.String("page", "", "serve this index.html instead of the built-in one (for editing the page)")
	_ = fs.Parse(args)
	if _, err := f.board(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		body := benchpage.Index
		if *page != "" {
			b, err := os.ReadFile(*page)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			body = b
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /data.json", func(w http.ResponseWriter, r *http.Request) {
		b, err := f.board()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b.Details = true
		writeJSON(w, b)
	})
	mux.HandleFunc("GET /run", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		trial, _ := strconv.Atoi(q.Get("trial"))
		if sealed, err := f.sealedTasks(); err != nil || sealed[q.Get("task")] {
			http.Error(w, "the held-out set's runs have no details", http.StatusForbidden)
			return
		}
		d, err := bench.LoadRunDetail(*f.results, q.Get("backend"), q.Get("task"), trial, q.Get("corrected") == "1")
		if err != nil {
			http.Error(w, "no such run", http.StatusNotFound)
			return
		}
		if d.Screenshot != "" {
			d.Screenshot = "shot/" + d.Screenshot
		}
		writeJSON(w, d)
	})
	mux.HandleFunc("GET /shot/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/shot/")
		p, ok := bench.ShotPath(*f.results, rel)
		if parts := strings.Split(rel, "/"); ok && len(parts) >= 3 {
			if sealed, err := f.sealedTasks(); err != nil || sealed[parts[1]+"/"+parts[2]] {
				ok = false
			}
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
	})
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("results page on http://%s/ (results %s)", ln.Addr(), *f.results)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// exportWeb writes a static snapshot: the page and data.json, with no
// transcripts or screenshots.
func exportWeb(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	f := addWebFlags(fs)
	out := fs.String("out", "bench-page", "output directory")
	_ = fs.Parse(args)
	b, err := f.board()
	if err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "index.html"), benchpage.Index, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "data.json"), data, 0o644); err != nil {
		return err
	}
	log.Printf("wrote %s/index.html and data.json (%d runs)", *out, len(b.Runs))
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
