package applog

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Entry is an extra file added to an export, such as a system summary.
type Entry struct {
	Name string
	Data []byte
}

// launchdTail bounds how much of daemon.log an export carries.
const launchdTail = 4 << 20

// keptExports is how many finished archives stay in the exports directory.
const keptExports = 3

// ExportDir holds finished archives until the UI moves them where the user
// chose. It is outside the log directory so exports never include exports.
func ExportDir(home string) string { return filepath.Join(home, ".config/fennec/exports") }

// diagnosticFiles are the small status files under ~/.config/fennec/diagnostics
// worth sending; profile configuration backups are left out on purpose.
var diagnosticFiles = []string{"entry-*.json", "launchers.json", "terminal-integration.json"}

// WriteExport builds an archive of the last Retention of logs plus extra and
// returns its path inside ExportDir.
func WriteExport(home string, now time.Time, extra []Entry) (string, error) {
	dir := ExportDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "fennec-logs-" + now.Format("20060102-150405") + ".zip"
	tmp, err := os.CreateTemp(dir, ".export-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := Export(home, tmp, now, extra); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	pruneExports(dir)
	return path, nil
}

func pruneExports(dir string) {
	paths, _ := filepath.Glob(filepath.Join(dir, "fennec-logs-*.zip"))
	sort.Strings(paths) // timestamped names sort oldest first
	for len(paths) > keptExports {
		os.Remove(paths[0])
		paths = paths[1:]
	}
}

// Export writes a zip of the daily logs, goroutine dumps and launchd stderr
// from the last Retention, the diagnostics status files, and extra. Every text
// file passes through Redact.
func Export(home string, out io.Writer, now time.Time, extra []Entry) error {
	archive := zip.NewWriter(out)
	add := func(name string, data []byte, modified time.Time) error {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified}
		w, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = w.Write(Redact(data))
		return err
	}
	for _, entry := range extra {
		if err := add(entry.Name, entry.Data, now); err != nil {
			return err
		}
	}
	cutoff := now.Add(-Retention)
	logs := Dir(home)
	paths := expirable(logs)
	sort.Strings(paths)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(cutoff) {
			continue
		}
		data, err := readLimited(path, DayLimit+(1<<20))
		if err != nil {
			continue
		}
		if err := add("logs/"+filepath.Base(path), data, info.ModTime()); err != nil {
			return err
		}
	}
	if info, err := os.Stat(filepath.Join(logs, LaunchdLog)); err == nil && info.Mode().IsRegular() {
		if data, err := readTail(filepath.Join(logs, LaunchdLog), launchdTail); err == nil {
			if err := add("logs/"+LaunchdLog, data, info.ModTime()); err != nil {
				return err
			}
		}
	}
	diagnostics := filepath.Join(home, ".config/fennec/diagnostics")
	for _, pattern := range diagnosticFiles {
		matches, _ := filepath.Glob(filepath.Join(diagnostics, pattern))
		sort.Strings(matches)
		for _, path := range matches {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			data, err := readLimited(path, 1<<20)
			if err != nil {
				continue
			}
			if err := add("diagnostics/"+filepath.Base(path), data, info.ModTime()); err != nil {
				return err
			}
		}
	}
	return archive.Close()
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= limit {
		return io.ReadAll(file)
	}
	data := make([]byte, limit)
	n, err := file.ReadAt(data, info.Size()-limit)
	if err != nil && err != io.EOF {
		return nil, err
	}
	data = data[:n]
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[i+1:]
	}
	return append([]byte(fmt.Sprintf("[last %d KB of %d KB]\n", len(data)>>10, info.Size()>>10)), data...), nil
}

var redactions = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[redacted private key]"},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)((?:access|refresh|id|auth|session|api)?[_-]?token|api[_-]?key|client[_-]?secret|secret|password|passwd)("?\s*[:=]\s*"?)[^\s"',&}]+`), "${1}${2}[redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[redacted jwt]"},
	{regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{4})[A-Za-z0-9_-]{12,}`), "${1}[redacted]"},
	{regexp.MustCompile(`\b(gh[pousr]_|xox[abprs]-|glpat-)[A-Za-z0-9_-]{10,}`), "${1}[redacted]"},
}

// Redact removes credentials that might have reached a log line by accident.
// Logs are not supposed to carry secrets; this is a second line of defense
// for files a user sends to someone else.
func Redact(data []byte) []byte {
	text := string(data)
	for _, r := range redactions {
		text = r.pattern.ReplaceAllString(text, r.replace)
	}
	if text == string(data) {
		return data
	}
	return []byte(text)
}

// SummaryLines renders key/value pairs as a stable text block.
func SummaryLines(pairs [][2]string) []byte {
	var b strings.Builder
	for _, p := range pairs {
		fmt.Fprintf(&b, "%s: %s\n", p[0], p[1])
	}
	return []byte(b.String())
}
