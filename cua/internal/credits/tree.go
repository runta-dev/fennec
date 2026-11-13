package credits

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Kind is what a tree entry is.
type Kind int

// Entry kinds.
const (
	KindNone Kind = iota
	KindDir
	KindFile
	KindSymlink
)

// DirEntry is one entry of a directory.
type DirEntry struct {
	Name string
	Kind Kind
}

// Tree is a read-only source checkout addressed by clean slash paths
// relative to its root ("" is the root). Symbolic links are reported, not
// followed; resolve follows them.
type Tree interface {
	Lstat(p string) Kind
	ReadDir(p string) ([]DirEntry, error) // sorted by name
	ReadFile(p string) ([]byte, error)
	Readlink(p string) (string, error)
}

// Prefetcher is a Tree that reads many files faster in one batch.
type Prefetcher interface {
	Prefetch(paths []string) error
}

// DirTree is a checkout on disk (a real Chromium checkout, or a test
// fixture).
type DirTree string

func (d DirTree) abs(p string) string { return filepath.Join(string(d), filepath.FromSlash(p)) }

// Lstat implements Tree.
func (d DirTree) Lstat(p string) Kind {
	fi, err := os.Lstat(d.abs(p))
	switch {
	case err != nil:
		return KindNone
	case fi.Mode()&fs.ModeSymlink != 0:
		return KindSymlink
	case fi.IsDir():
		return KindDir
	}
	return KindFile
}

// ReadDir implements Tree.
func (d DirTree) ReadDir(p string) ([]DirEntry, error) {
	es, err := os.ReadDir(d.abs(p))
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(es))
	for _, e := range es {
		k := KindFile
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			k = KindSymlink
		case e.IsDir():
			k = KindDir
		}
		out = append(out, DirEntry{e.Name(), k})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadFile implements Tree.
func (d DirTree) ReadFile(p string) ([]byte, error) { return os.ReadFile(d.abs(p)) }

// Readlink implements Tree.
func (d DirTree) Readlink(p string) (string, error) { return os.Readlink(d.abs(p)) }

// errOutside is a path that leaves the tree.
var errOutside = errors.New("the path leaves the source tree")

// resolve follows symbolic links in every component of a clean path and
// answers the physical path and its kind (KindNone when it does not
// exist). Links that leave the tree resolve to nothing.
func resolve(t Tree, p string) (string, Kind, error) {
	comps := splitPath(p)
	cur, kind, hops := "", KindDir, 0
	for len(comps) > 0 {
		c := comps[0]
		comps = comps[1:]
		if kind != KindDir {
			return "", KindNone, nil // a component under a file
		}
		switch c {
		case ".":
			continue
		case "..":
			if cur == "" {
				return "", KindNone, nil
			}
			if cur = path.Dir(cur); cur == "." {
				cur = ""
			}
			continue
		}
		next := path.Join(cur, c)
		switch k := t.Lstat(next); k {
		case KindNone:
			return "", KindNone, nil
		case KindSymlink:
			if hops++; hops > 40 {
				return "", KindNone, errors.New("too many levels of symbolic links")
			}
			target, err := t.Readlink(next)
			if err != nil {
				return "", KindNone, err
			}
			if path.IsAbs(target) {
				return "", KindNone, nil
			}
			comps = append(splitPath(target), comps...)
		default:
			cur, kind = next, k
		}
	}
	return cur, kind, nil
}

func splitPath(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// readText reads a file as Python's read_text(encoding="utf-8",
// errors="replace") does: invalid UTF-8 becomes U+FFFD (one per maximal
// invalid subsequence) and newlines are universal ("\r\n" and "\r" read
// as "\n").
func readText(t Tree, p string) (string, error) {
	rp, k, err := resolve(t, p)
	if err != nil {
		return "", err
	}
	if k != KindFile {
		return "", &fs.PathError{Op: "read", Path: p, Err: fs.ErrNotExist}
	}
	data, err := t.ReadFile(rp)
	if err != nil {
		return "", err
	}
	s := decodeUTF8Replace(data)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// decodeUTF8Replace decodes as Python's UTF-8 decoder with
// errors="replace": each maximal subpart of an ill-formed sequence
// becomes one U+FFFD.
func decodeUTF8Replace(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		n, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			n = 1
		case c == 0xE0:
			n, lo = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			n = 2
		case c == 0xED:
			n, hi = 2, 0x9F
		case c == 0xF0:
			n, lo = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			n = 3
		case c == 0xF4:
			n, hi = 3, 0x8F
		default:
			sb.WriteString("�")
			i++
			continue
		}
		j := i + 1
		ok := true
		for k := 0; k < n; k++ {
			if j >= len(b) {
				ok = false
				break
			}
			l, h := byte(0x80), byte(0xBF)
			if k == 0 {
				l, h = lo, hi
			}
			if b[j] < l || b[j] > h {
				ok = false
				break
			}
			j++
		}
		if ok {
			sb.Write(b[i:j])
		} else {
			sb.WriteString("�")
		}
		i = j
	}
	return sb.String()
}
