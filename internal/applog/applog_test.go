package applog

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterRotatesDailyAndPrunesAfterRetention(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 11, 20, 23, 59, 0, 0, time.Local)
	old := filepath.Join(dir, "fennec-2026-11-10.log")
	oldDump := filepath.Join(dir, "goroutines-1-20261110T000000.000.txt")
	recent := filepath.Join(dir, "fennec-2026-11-15.log")
	unrelated := filepath.Join(dir, "daemon.log")
	for _, path := range []string{old, oldDump, recent, unrelated} {
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := day.Add(-8 * 24 * time.Hour)
	for _, path := range []string{old, oldDump, unrelated} {
		os.Chtimes(path, stale, stale)
	}
	fresh := day.Add(-5 * 24 * time.Hour)
	os.Chtimes(recent, fresh, fresh)

	w := NewWriter(dir)
	w.now = func() time.Time { return day }
	w.Write([]byte("first\n"))
	day = day.Add(2 * time.Minute) // next local day
	w.Write([]byte("second\n"))
	w.Close()

	for path, want := range map[string]string{
		filepath.Join(dir, "fennec-2026-11-20.log"): "first\n",
		filepath.Join(dir, "fennec-2026-11-21.log"): "second\n",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", filepath.Base(path), data, err, want)
		}
	}
	for _, path := range []string{old, oldDump} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should be pruned", filepath.Base(path))
		}
	}
	for _, path := range []string{recent, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should be kept: %v", filepath.Base(path), err)
		}
	}
}

func TestWriterStopsAtDayLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	path := filepath.Join(dir, "fennec-2026-11-25.log")
	if err := os.WriteFile(path, make([]byte, DayLimit-4), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(dir)
	w.now = func() time.Time { return now }
	w.Write([]byte("too long\n"))
	w.Write([]byte("again\n"))
	w.Close()
	data, _ := os.ReadFile(path)
	tail := string(data[DayLimit-4:])
	if strings.Count(tail, "log limit") != 1 || strings.Contains(tail, "too long") || strings.Contains(tail, "again") {
		t.Fatalf("unexpected tail %q", tail)
	}
}

func TestTrimFileKeepsWholeLinesAtTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("line number ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	b.WriteString("LAST\n")
	os.WriteFile(path, []byte(b.String()), 0o600)
	if err := TrimFile(path, 1000, 200); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if !strings.HasPrefix(lines[0], "[trimmed") || lines[len(lines)-1] != "LAST" || len(data) > 300 {
		t.Fatalf("trimmed = %q", data)
	}
	for _, line := range lines[1:] {
		if line != "LAST" && !strings.HasPrefix(line, "line number ") {
			t.Fatalf("partial line kept: %q", line)
		}
	}
	before, _ := os.ReadFile(path)
	TrimFile(path, 1000, 200)
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("a file under the limit must not change")
	}
}

func TestExportIncludesRecentLogsAndRedacts(t *testing.T) {
	home := t.TempDir()
	logs := Dir(home)
	diagnostics := filepath.Join(home, ".config/fennec/diagnostics")
	os.MkdirAll(logs, 0o700)
	os.MkdirAll(diagnostics, 0o700)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	write := func(path, text string, at time.Time) {
		os.WriteFile(path, []byte(text), 0o600)
		os.Chtimes(path, at, at)
	}
	write(filepath.Join(logs, "fennec-2026-11-25.log"), "prepare failed: Runta HTTP 500\nAuthorization: Bearer abcdefghijklmnop\n", now)
	write(filepath.Join(logs, "fennec-2026-11-01.log"), "too old\n", now.Add(-24*24*time.Hour))
	write(filepath.Join(logs, "daemon.log"), "panic: boom\n", now)
	write(filepath.Join(diagnostics, "entry-claude.json"), `{"agent":"claude","token":"secretvalue123"}`, now)
	write(filepath.Join(diagnostics, "profile-configuration-backup-1.json"), "{}", now)

	path, err := WriteExport(home, now, []Entry{{Name: "system.txt", Data: []byte("app_version: 1.2.3\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != ExportDir(home) || filepath.Base(path) != "fennec-logs-20261125-120000.zip" {
		t.Fatalf("path = %s", path)
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	files := map[string]string{}
	for _, f := range reader.File {
		r, _ := f.Open()
		data, _ := io.ReadAll(r)
		r.Close()
		files[f.Name] = string(data)
	}
	want := []string{"system.txt", "logs/fennec-2026-11-25.log", "logs/daemon.log", "diagnostics/entry-claude.json"}
	if len(files) != len(want) {
		t.Fatalf("files = %v", keys(files))
	}
	for _, name := range want {
		if _, ok := files[name]; !ok {
			t.Fatalf("missing %s in %v", name, keys(files))
		}
	}
	if day := files["logs/fennec-2026-11-25.log"]; !strings.Contains(day, "Runta HTTP 500") || strings.Contains(day, "abcdefghijklmnop") {
		t.Fatalf("day log = %q", day)
	}
	if entry := files["diagnostics/entry-claude.json"]; strings.Contains(entry, "secretvalue123") || !strings.Contains(entry, `"agent":"claude"`) {
		t.Fatalf("entry = %q", entry)
	}
}

func TestExportKeepsOnlyRecentArchives(t *testing.T) {
	home := t.TempDir()
	start := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	for i := 0; i < 5; i++ {
		if _, err := WriteExport(home, start.Add(time.Duration(i)*time.Second), nil); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(ExportDir(home), "*"))
	if len(paths) != keptExports || filepath.Base(paths[0]) != "fennec-logs-20261125-120002.zip" {
		t.Fatalf("exports = %v", paths)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer abc.def-ghi_jkl":              "Authorization: Bearer [redacted]",
		`{"access_token":"xyz123abc"}`:                       `{"access_token":"[redacted]"}`,
		"api_key=sk-ant-0123456789abcdefghij other":          "api_key=[redacted] other",
		"key sk-ant-0123456789abcdefghij":                    "key sk-ant-[redacted]",
		"token ghp_0123456789abcdefghijABCD":                 "token ghp_[redacted]",
		"queue sync recovered after 5s":                      "queue sync recovered after 5s",
		"jwt eyJhbGciOiJI.eyJzdWIiOiIx.SflKxwRJSMeKKF2QT4fw": "jwt [redacted jwt]",
		"password: hunter22":                                 "password: [redacted]",
	}
	for in, want := range cases {
		if got := string(Redact([]byte(in))); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
