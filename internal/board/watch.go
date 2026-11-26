package board

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Watch serves `fennec-terminal board-watch` (FEN-60): one long-running
// command that pushes board changes to a client (Fennec for iPhone) over a
// single ssh channel, instead of the client polling `board-call`.
//
// The client writes subscriptions to standard input, one JSON object per
// line, whenever what it shows changes:
//
//	{"project":"fennec-c98514","thread":60}   // thread 0 or absent: no thread
//
// Watch writes events to out, one JSON object per line:
//
//	{"type":"projects","projects":[...]}                        same rows as GET /v1/projects
//	{"type":"requests","project":"p","reset":true,"requests":[...]}  the whole list (first time, or after a switch)
//	{"type":"requests","project":"p","order":[3,1,2],"changed":[...]}
//	    rows that changed or are new, and the list's numbers in order;
//	    numbers missing from order are gone
//	{"type":"thread","project":"p","number":60,"thread":{...}}  same as GET /v1/projects/p/requests/60
//	{"type":"thread","project":"p","number":60,"error":"not found"}
//	{"type":"ping"}                                              every WatchPing with nothing else to say
//
// Only what changed is sent. Watch looks at the board's files every
// WatchTick (a few dozen stats) and reads the board again only when one
// changed, or every WatchRescan for state that isn't in those files. It
// returns when ctx ends, standard input closes, or a write fails.
func Watch(ctx context.Context, home string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &watcher{store: Store{Home: home, Role: "cloud"}, out: out, rows: map[int][]byte{}}
	subs := make(chan watchSub, 1)
	go func() {
		defer cancel()
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			var sub watchSub
			if json.Unmarshal(scanner.Bytes(), &sub) != nil || (sub.Project != "" && !projectID.MatchString(sub.Project)) || sub.Thread < 0 {
				continue
			}
			select {
			case <-subs: // only the latest matters
			default:
			}
			subs <- sub
		}
	}()
	tick := time.NewTicker(WatchTick)
	defer tick.Stop()
	var print string
	lastScan, lastSent := time.Time{}, time.Now()
	for {
		changed := false
		select {
		case <-ctx.Done():
			return nil
		case sub := <-subs:
			changed = w.subscribe(sub)
		case <-tick.C:
		}
		now := time.Now()
		if p := w.store.fingerprint(w.sub.Project); changed || p != print || now.Sub(lastScan) >= WatchRescan {
			print, lastScan = p, now
			sent, err := w.scan()
			if err != nil {
				return err
			}
			if sent {
				lastSent = now
			}
		}
		if now.Sub(lastSent) >= WatchPing {
			if err := w.send(map[string]string{"type": "ping"}); err != nil {
				return err
			}
			lastSent = now
		}
	}
}

// How often Watch looks for changes, reads the board regardless, and pings.
var (
	WatchTick   = 250 * time.Millisecond
	WatchRescan = 5 * time.Second
	WatchPing   = 20 * time.Second
)

type watchSub struct {
	Project string `json:"project"`
	Thread  int    `json:"thread,omitempty"`
}

type watcher struct {
	store    Store
	out      io.Writer
	mu       sync.Mutex
	sub      watchSub
	projects []byte
	// rows are the open queue's rows as last sent, by number; order is
	// their order. listed says a full list was sent for sub.Project.
	rows   map[int][]byte
	order  []int
	listed bool
	thread []byte
}

// subscribe switches what is watched; true when it differs.
func (w *watcher) subscribe(sub watchSub) bool {
	if sub == w.sub {
		return false
	}
	if sub.Project != w.sub.Project {
		w.rows, w.order, w.listed = map[int][]byte{}, nil, false
	}
	if sub.Project != w.sub.Project || sub.Thread != w.sub.Thread {
		w.thread = nil
	}
	w.sub = sub
	return true
}

// scan reads the board and sends what changed since the last scan.
func (w *watcher) scan() (bool, error) {
	sent := false
	if body, ok := w.get("/v1/projects"); ok && !bytes.Equal(body, w.projects) {
		w.projects = body
		if err := w.send(map[string]any{"type": "projects", "projects": json.RawMessage(body)}); err != nil {
			return sent, err
		}
		sent = true
	}
	if p := w.sub.Project; p != "" {
		if body, ok := w.get("/v1/projects/" + p + "/requests"); ok {
			did, err := w.requests(p, body)
			if err != nil {
				return sent, err
			}
			sent = sent || did
		}
		if n := w.sub.Thread; n > 0 {
			path := "/v1/projects/" + p + "/requests/" + strconv.Itoa(n)
			event := map[string]any{"type": "thread", "project": p, "number": n}
			body, code := w.call(path)
			switch {
			case code == http.StatusOK:
				event["thread"] = json.RawMessage(body)
			case code == http.StatusNotFound:
				event["error"] = "not found"
				body = []byte("404")
			default:
				return sent, nil // try again at the next scan
			}
			if !bytes.Equal(body, w.thread) {
				w.thread = body
				if err := w.send(event); err != nil {
					return sent, err
				}
				sent = true
			}
		}
	}
	return sent, nil
}

// requests sends the open queue's list: whole the first time, then only
// the rows that changed and the order.
func (w *watcher) requests(project string, body []byte) (bool, error) {
	var list []json.RawMessage
	if json.Unmarshal(body, &list) != nil {
		return false, nil
	}
	rows := make(map[int][]byte, len(list))
	order := make([]int, 0, len(list))
	var changed []json.RawMessage
	for _, raw := range list {
		var head struct {
			Number int `json:"number"`
		}
		if json.Unmarshal(raw, &head) != nil || head.Number < 1 {
			continue
		}
		rows[head.Number] = raw
		order = append(order, head.Number)
		if !bytes.Equal(w.rows[head.Number], raw) {
			changed = append(changed, raw)
		}
	}
	reset := !w.listed
	same := len(changed) == 0 && len(order) == len(w.order)
	for i := 0; same && i < len(order); i++ {
		same = order[i] == w.order[i]
	}
	if !reset && same {
		return false, nil
	}
	w.rows, w.order, w.listed = rows, order, true
	if reset {
		return true, w.send(map[string]any{"type": "requests", "project": project, "reset": true, "requests": json.RawMessage(body)})
	}
	if changed == nil {
		changed = []json.RawMessage{}
	}
	return true, w.send(map[string]any{"type": "requests", "project": project, "order": order, "changed": changed})
}

// get reads one board API path the way board-call does.
func (w *watcher) get(path string) ([]byte, bool) {
	body, code := w.call(path)
	return body, code == http.StatusOK
}

func (w *watcher) call(path string) ([]byte, int) {
	r := httptest.NewRequest(http.MethodGet, "http://board.fennec"+path, nil)
	rec := httptest.NewRecorder()
	w.store.Handler().ServeHTTP(rec, r)
	return bytes.TrimSpace(rec.Body.Bytes()), rec.Code
}

func (w *watcher) send(event any) error {
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.out.Write(append(line, '\n')); err != nil {
		return err
	}
	if f, ok := w.out.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// fingerprint is a cheap summary of the files the watched views come from:
// size and modification time of the board's own files, every project, and
// the watched project's requests. Writes go through rename, which also
// touches the directory, so a new or deleted request changes it too.
func (s Store) fingerprint(project string) string {
	var b bytes.Buffer
	stat := func(path string) {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", filepath.Base(path), info.Size(), info.ModTime().UnixNano())
		}
	}
	dir := func(path string, files bool) {
		stat(path)
		if !files {
			return
		}
		entries, _ := os.ReadDir(path)
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s %d %d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	root := s.root()
	dir(root, true)
	dir(filepath.Join(root, "deliveries"), true)
	dir(filepath.Join(root, "session-locks"), true)
	projects := filepath.Join(root, "projects")
	entries, _ := os.ReadDir(projects)
	stat(projects)
	for _, e := range entries {
		stat(filepath.Join(projects, e.Name(), "project.json"))
		dir(filepath.Join(projects, e.Name(), "requests"), e.Name() == project)
	}
	return b.String()
}
