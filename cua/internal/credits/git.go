package credits

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ChromiumSrc is the chromium/src repository.
const ChromiumSrc = "https://chromium.googlesource.com/chromium/src"

// Fetcher reads a Chromium commit and its git dependencies from their
// servers into a cache of bare, shallow, blobless repositories: trees
// first, then only the blobs credits need (metadata files, licence texts,
// symbolic links and DEPS files), in one batch per repository. It runs
// the git command; nothing is checked out.
type Fetcher struct {
	Cache string // directory for the repositories (never the source tree)
	Jobs  int    // parallel repositories; zero means 8
	Log   func(format string, args ...any)
	// SrcURL is chromium/src; empty means ChromiumSrc.
	SrcURL string

	mu    sync.Mutex
	repos map[string]*repo
	// allowFile admits file:// repositories (tests).
	allowFile bool
	logMu     sync.Mutex
}

// logf calls Log one at a time (fetches run in parallel).
func (f *Fetcher) logf(format string, args ...any) {
	if f.Log != nil {
		f.logMu.Lock()
		defer f.logMu.Unlock()
		f.Log(format, args...)
	}
}

// repo is one cached bare repository.
type repo struct {
	f   *Fetcher
	url string
	dir string

	mu    sync.Mutex // guards blobs
	blobs map[string][]byte
	// op serialises git commands that write the repository.
	op sync.Mutex
}

func (f *Fetcher) repo(u string) (*repo, error) {
	pu, err := url.Parse(u)
	local := f.allowFile && err == nil && pu.Scheme == "file"
	if !local && (err != nil || pu.Scheme != "https" || pu.Host == "") {
		return nil, fmt.Errorf("not an https repository: %q", u)
	}
	rel := path.Clean("/" + pu.Path)
	if strings.Contains(rel, "..") {
		return nil, fmt.Errorf("bad repository path %q", u)
	}
	host := pu.Host
	if local {
		host = "file"
	}
	dir := filepath.Join(f.Cache, "git", host, filepath.FromSlash(strings.TrimPrefix(rel, "/"))+".git")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.repos[dir]; ok {
		return r, nil
	}
	if f.repos == nil {
		f.repos = map[string]*repo{}
	}
	r := &repo{f: f, url: u, dir: dir, blobs: map[string][]byte{}}
	f.repos[dir] = r
	return r, nil
}

func (r *repo) git(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.dir}, args...)...)
	// Never fetch lazily (one request per object) and never prompt.
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("git %s (%s): %v: %s", args[0], r.url, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// ensure has the commit's trees (no blobs) in the cache.
func (r *repo) ensure(ctx context.Context, commit string) error {
	r.op.Lock()
	defer r.op.Unlock()
	if _, err := os.Stat(filepath.Join(r.dir, "HEAD")); err != nil {
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			return err
		}
		if _, err := r.git(ctx, nil, "init", "-q", "--bare"); err != nil {
			return err
		}
	}
	if _, err := r.git(ctx, nil, "cat-file", "-e", commit+"^{tree}"); err == nil {
		return nil
	}
	r.f.logf("credits: fetching the tree of %s at %s", r.url, short(commit))
	_, err := r.git(ctx, nil, "-c", "protocol.version=2", "fetch", "-q", "--depth=1", "--filter=blob:none", "--no-tags", "--no-write-fetch-head", r.url, commit)
	return err
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

type lsEntry struct {
	mode, typ, oid, path string
}

func (r *repo) lsTree(ctx context.Context, commit string) ([]lsEntry, error) {
	out, err := r.git(ctx, nil, "ls-tree", "-r", "-t", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	var es []lsEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("ls-tree: unexpected %q", rec)
		}
		es = append(es, lsEntry{f[0], f[1], f[2], string(p)})
	}
	return es, nil
}

// load makes the blobs available in memory, fetching the missing ones
// in one request.
func (r *repo) load(ctx context.Context, oids []string) error {
	r.op.Lock()
	defer r.op.Unlock()
	r.mu.Lock()
	var want []string
	seen := map[string]bool{}
	for _, o := range oids {
		if _, ok := r.blobs[o]; !ok && !seen[o] {
			want = append(want, o)
			seen[o] = true
		}
	}
	r.mu.Unlock()
	if len(want) == 0 {
		return nil
	}
	sort.Strings(want)
	check, err := r.git(ctx, strings.NewReader(strings.Join(want, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return err
	}
	var missing []string
	for _, line := range strings.Split(string(check), "\n") {
		if o, ok := strings.CutSuffix(line, " missing"); ok {
			missing = append(missing, o)
		}
	}
	if len(missing) > 0 {
		r.f.logf("credits: fetching %d files from %s", len(missing), r.url)
		for i := 0; i < len(missing); i += 4000 {
			part := missing[i:min(i+4000, len(missing))]
			if _, err := r.git(ctx, strings.NewReader(strings.Join(part, "\n")+"\n"),
				"-c", "protocol.version=2", "-c", "fetch.negotiationAlgorithm=noop", "fetch", "-q", "--no-tags",
				"--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "--stdin", r.url); err != nil {
				return err
			}
		}
	}
	out, err := r.git(ctx, strings.NewReader(strings.Join(want, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return err
	}
	br := bufio.NewReader(bytes.NewReader(out))
	got := map[string][]byte{}
	for {
		hdr, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		f := strings.Fields(hdr)
		if len(f) != 3 || f[1] != "blob" {
			return fmt.Errorf("cat-file: unexpected %q from %s", strings.TrimSpace(hdr), r.url)
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return err
		}
		data := make([]byte, n+1)
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		got[f[0]] = data[:n]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range want {
		d, ok := got[o]
		if !ok {
			return fmt.Errorf("%s did not deliver blob %s", r.url, o)
		}
		r.blobs[o] = d
	}
	return nil
}

func (r *repo) blob(o string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.blobs[o]
	return d, ok
}

// gnode is one entry of a GitTree.
type gnode struct {
	kind Kind
	oid  string
	repo *repo
	kids map[string]*gnode
}

// GitTree is a commit of chromium/src with its git dependencies mounted
// where DEPS puts them for the platform.
type GitTree struct {
	ctx  context.Context
	root *gnode
	// Deps are the dependencies DEPS checks out for the platform.
	Deps []Dep
	// Unfetched are the CIPD and GCS dependencies (prebuilt archives);
	// their contents are not read.
	Unfetched []Dep
	// Unevaluated are conditions that could not be evaluated.
	Unevaluated []string
}

func (t *GitTree) node(p string) *gnode {
	n := t.root
	for _, c := range splitPath(p) {
		if n.kind != KindDir || n.kids == nil {
			return nil
		}
		if n = n.kids[c]; n == nil {
			return nil
		}
	}
	return n
}

// Lstat implements Tree.
func (t *GitTree) Lstat(p string) Kind {
	if n := t.node(p); n != nil {
		return n.kind
	}
	return KindNone
}

// ReadDir implements Tree.
func (t *GitTree) ReadDir(p string) ([]DirEntry, error) {
	n := t.node(p)
	if n == nil || n.kind != KindDir {
		return nil, fmt.Errorf("%s: not a directory", p)
	}
	out := make([]DirEntry, 0, len(n.kids))
	for name, k := range n.kids {
		out = append(out, DirEntry{name, k.kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (t *GitTree) data(p string) ([]byte, error) {
	n := t.node(p)
	if n == nil || n.kind == KindDir {
		return nil, fmt.Errorf("%s: not a file", p)
	}
	if d, ok := n.repo.blob(n.oid); ok {
		return d, nil
	}
	if err := n.repo.load(t.ctx, []string{n.oid}); err != nil {
		return nil, err
	}
	d, _ := n.repo.blob(n.oid)
	return d, nil
}

// ReadFile implements Tree.
func (t *GitTree) ReadFile(p string) ([]byte, error) { return t.data(p) }

// Readlink implements Tree.
func (t *GitTree) Readlink(p string) (string, error) {
	d, err := t.data(p)
	return string(d), err
}

// Prefetch implements Prefetcher: the files (after links) are loaded in
// one batch per repository.
func (t *GitTree) Prefetch(paths []string) error {
	byRepo := map[*repo][]string{}
	for _, p := range paths {
		rp, k, err := resolve(t, p)
		if err != nil || k != KindFile {
			continue
		}
		n := t.node(rp)
		byRepo[n.repo] = append(byRepo[n.repo], n.oid)
	}
	return parallel(t.ctx, jobsOf(nil), mapKeys(byRepo), func(ctx context.Context, r *repo) error {
		return r.load(ctx, byRepo[r])
	})
}

func mapKeys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func jobsOf(f *Fetcher) int {
	if f == nil || f.Jobs <= 0 {
		return 8
	}
	return f.Jobs
}

// parallel runs fn over items with at most n at once and answers the
// first error.
func parallel[T any](ctx context.Context, n int, items []T, fn func(context.Context, T) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			if err := fn(ctx, it); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}()
	}
	wg.Wait()
	return first
}

// mount puts a repository's tree at a path, replacing what was there (a
// submodule link).
func (t *GitTree) mount(at string, r *repo, es []lsEntry) error {
	parent := t.root
	comps := splitPath(at)
	for i, c := range comps {
		if parent.kind != KindDir {
			return fmt.Errorf("cannot mount %s: %s is not a directory", at, strings.Join(comps[:i], "/"))
		}
		if parent.kids == nil {
			parent.kids = map[string]*gnode{}
		}
		if i == len(comps)-1 {
			break
		}
		next := parent.kids[c]
		if next == nil {
			next = &gnode{kind: KindDir}
			parent.kids[c] = next
		}
		parent = next
	}
	top := &gnode{kind: KindDir, repo: r}
	if len(comps) == 0 {
		t.root = top
	} else {
		parent.kids[comps[len(comps)-1]] = top
	}
	dirs := map[string]*gnode{"": top}
	for _, e := range es {
		d := path.Dir(e.path)
		if d == "." {
			d = ""
		}
		pn := dirs[d]
		if pn == nil {
			return fmt.Errorf("%s: %s listed before its directory", r.url, e.path)
		}
		n := &gnode{oid: e.oid, repo: r}
		switch {
		case e.typ == "tree":
			n.kind = KindDir
			dirs[e.path] = n
		case e.typ == "commit":
			n.kind = KindDir // a submodule: an empty directory until mounted
		case e.mode == "120000":
			n.kind = KindSymlink
		default:
			n.kind = KindFile
		}
		if pn.kids == nil {
			pn.kids = map[string]*gnode{}
		}
		pn.kids[path.Base(e.path)] = n
	}
	return nil
}

// Tree reads a chromium/src commit and every git dependency its DEPS (and
// the DEPS of the dependencies it recurses into) checks out for the
// platform, at the revisions DEPS pins.
func (f *Fetcher) Tree(ctx context.Context, commit string, pl Platform) (*GitTree, error) {
	if f.Cache == "" {
		return nil, errors.New("the credits fetcher needs a cache directory")
	}
	t := &GitTree{ctx: ctx, root: &gnode{kind: KindDir}}
	srcURL := f.SrcURL
	if srcURL == "" {
		srcURL = ChromiumSrc
	}
	src, err := f.repo(srcURL)
	if err != nil {
		return nil, err
	}
	if err := src.ensure(ctx, commit); err != nil {
		return nil, err
	}
	es, err := src.lsTree(ctx, commit)
	if err != nil {
		return nil, err
	}
	if err := t.mount("", src, es); err != nil {
		return nil, err
	}
	type level struct {
		depsPath string // the DEPS file in the tree
		prefix   string // its checkout path from the gclient root
		vars     map[string]any
	}
	queue := []level{{depsPath: "DEPS", prefix: "src"}}
	declared := map[string]bool{}
	for len(queue) > 0 {
		lv := queue[0]
		queue = queue[1:]
		if t.Lstat(lv.depsPath) != KindFile {
			continue
		}
		text, err := readText(t, lv.depsPath)
		if err != nil {
			return nil, err
		}
		df, err := evalDEPS(text, lv.vars, pl, lv.prefix)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lv.depsPath, err)
		}
		t.Unevaluated = append(t.Unevaluated, df.unevaluated...)
		var gits []Dep
		for _, d := range df.deps {
			if declared[d.Path] {
				continue // a parent's declaration wins
			}
			declared[d.Path] = true
			if d.Type != "git" {
				t.Unfetched = append(t.Unfetched, d)
				continue
			}
			gits = append(gits, d)
		}
		type fetched struct {
			dep Dep
			r   *repo
			es  []lsEntry
		}
		results := make([]fetched, len(gits))
		idx := make([]int, len(gits))
		for i := range idx {
			idx[i] = i
		}
		err = parallel(ctx, jobsOf(f), idx, func(ctx context.Context, i int) error {
			d := gits[i]
			r, err := f.repo(d.URL)
			if err != nil {
				return fmt.Errorf("%s: %w", d.Path, err)
			}
			if err := r.ensure(ctx, d.Revision); err != nil {
				return fmt.Errorf("%s: %w", d.Path, err)
			}
			es, err := r.lsTree(ctx, d.Revision)
			if err != nil {
				return err
			}
			results[i] = fetched{d, r, es}
			return nil
		})
		if err != nil {
			return nil, err
		}
		// Shallower paths first, so a nested dependency lands inside its
		// parent's tree.
		sort.Slice(results, func(i, j int) bool {
			return strings.Count(results[i].dep.Path, "/") < strings.Count(results[j].dep.Path, "/")
		})
		for _, res := range results {
			if err := t.mount(res.dep.Path, res.r, res.es); err != nil {
				return nil, err
			}
			t.Deps = append(t.Deps, res.dep)
			if res.dep.Recurse {
				queue = append(queue, level{depsPath: path.Join(res.dep.Path, "DEPS"), prefix: "src/" + res.dep.Path, vars: df.vars})
			}
		}
	}
	sort.Slice(t.Deps, func(i, j int) bool { return t.Deps[i].Path < t.Deps[j].Path })
	// Symbolic links are read while walking: load them all now.
	var links []string
	var collect func(p string, n *gnode)
	collect = func(p string, n *gnode) {
		if n.kind == KindSymlink {
			links = append(links, p)
		}
		for name, k := range n.kids {
			collect(path.Join(p, name), k)
		}
	}
	collect("", t.root)
	byRepo := map[*repo][]string{}
	for _, p := range links {
		n := t.node(p)
		byRepo[n.repo] = append(byRepo[n.repo], n.oid)
	}
	if err := parallel(ctx, jobsOf(f), mapKeys(byRepo), func(ctx context.Context, r *repo) error {
		return r.load(ctx, byRepo[r])
	}); err != nil {
		return nil, err
	}
	return t, nil
}
