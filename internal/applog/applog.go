// Package applog keeps the daemon's local log: one file per local day in
// ~/.config/fennec/logs, deleted after Retention, and exportable as a zip a
// user can send when something fails on their Mac (THI-145).
package applog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Retention is how long daily logs and goroutine dumps are kept.
const Retention = 7 * 24 * time.Hour

// DayLimit caps one day's file so a log storm cannot fill the disk.
const DayLimit = 32 << 20

// LaunchdLog is the daemon's stderr file, written by launchd. It only
// receives panics and stray stderr output once Install has moved the logger,
// but it was unbounded before and launchd keeps appending to it.
const LaunchdLog = "daemon.log"

const (
	dayPrefix = "fennec-"
	daySuffix = ".log"
	dumpGlob  = "goroutines-*.txt"
)

// Dir is the log directory under home.
func Dir(home string) string { return filepath.Join(home, ".config/fennec/logs") }

// Writer appends to fennec-<YYYY-MM-DD>.log, switching files when the local
// date changes and pruning expired files when it does.
type Writer struct {
	mu      sync.Mutex
	dir     string
	now     func() time.Time
	day     string
	file    *os.File
	size    int64
	limited bool
}

// NewWriter returns a Writer for dir. Files are opened lazily on first write.
func NewWriter(dir string) *Writer { return &Writer{dir: dir, now: time.Now} }

// Write never fails: a logging problem must not break the caller.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if day := now.Format("2006-01-02"); day != w.day || w.file == nil {
		w.rotate(day, now)
	}
	if w.file == nil {
		return len(p), nil
	}
	if w.size+int64(len(p)) > DayLimit {
		if !w.limited {
			w.limited = true
			note := fmt.Sprintf("%s log limit of %d MB reached for today; further lines are dropped\n", now.Format("2006/01/02 15:04:05"), DayLimit>>20)
			n, _ := w.file.WriteString(note)
			w.size += int64(n)
		}
		return len(p), nil
	}
	n, _ := w.file.Write(p)
	w.size += int64(n)
	return len(p), nil
}

func (w *Writer) rotate(day string, now time.Time) {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	w.day, w.size, w.limited = day, 0, false
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(w.dir, dayPrefix+day+daySuffix), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if info, err := file.Stat(); err == nil {
		w.size = info.Size()
		w.limited = w.size >= DayLimit
	}
	w.file = file
	Prune(w.dir, now)
}

// Close closes the current file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Prune deletes daily logs and goroutine dumps last modified before
// now-Retention. The current day's file is always kept.
func Prune(dir string, now time.Time) {
	cutoff := now.Add(-Retention)
	today := dayPrefix + now.Format("2006-01-02") + daySuffix
	for _, path := range expirable(dir) {
		if filepath.Base(path) == today {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(path)
		}
	}
}

func expirable(dir string) []string {
	days, _ := filepath.Glob(filepath.Join(dir, dayPrefix+"*"+daySuffix))
	dumps, _ := filepath.Glob(filepath.Join(dir, dumpGlob))
	return append(days, dumps...)
}

// TrimFile keeps only the last keep bytes of path once it is larger than
// limit. launchd holds the file open with O_APPEND, so truncating in place
// keeps its later writes at the new end.
func TrimFile(path string, limit, keep int64) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= limit {
		return nil
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	tail := make([]byte, keep)
	n, err := file.ReadAt(tail, info.Size()-keep)
	if err != nil && err != io.EOF {
		return err
	}
	tail = tail[:n]
	if i := strings.IndexByte(string(tail), '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err = file.WriteAt(append([]byte(fmt.Sprintf("[trimmed to the last %d KB on %s]\n", keep>>10, time.Now().Format(time.RFC3339))), tail...), 0)
	return err
}

// Install sends the standard logger to the daily log under home and caps the
// launchd stderr file. It returns the Writer so the caller can close it.
func Install(home string) *Writer {
	dir := Dir(home)
	_ = os.MkdirAll(dir, 0o700)
	_ = TrimFile(filepath.Join(dir, LaunchdLog), 8<<20, 1<<20)
	w := NewWriter(dir)
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return w
}
