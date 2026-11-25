package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	"fennec/internal/cloudarchive"
	"fennec/internal/commandstream"
	"fennec/internal/control"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Cloud archive (THI-165): the user exports the cloud computer's queues and
// sessions to a file on the Mac and imports that file on another cloud
// computer. The cloud does the work (`fennec-terminal archive`); the daemon
// carries the stream over the resident command socket and runs one job at a
// time in the background, so the Settings pages can poll its progress.
//
//	cloud.archive.export  {sessions}  start an export into ~/.config/fennec/exports
//	cloud.archive.import  {path}      start importing a .fenarchive file
//	cloud.archive.inspect {path}      read an archive's manifest (preview)
//	cloud.archive.plan                what an export would hold (THI-188)
//	cloud.archive.status              the current or last job
//	cloud.archive.cancel              stop the current job
type archiveControl struct {
	s   *daemonServices
	mu  sync.Mutex
	job *archiveJob
}

type archiveJob struct {
	Kind     string                 `json:"kind"`  // export | import
	State    string                 `json:"state"` // running | done | failed | cancelled
	Bytes    int64                  `json:"bytes"`
	Total    int64                  `json:"total,omitempty"`
	Path     string                 `json:"path,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Manifest *cloudarchive.Manifest `json:"manifest,omitempty"`
	Result   *cloudarchive.Result   `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Started  time.Time              `json:"started"`
	Finished *time.Time             `json:"finished,omitempty"`

	progress *atomic.Int64
	cancel   context.CancelFunc
}

func newArchiveControl(s *daemonServices) *archiveControl { return &archiveControl{s: s} }

func (c *archiveControl) handlers() map[string]control.Handler {
	return map[string]control.Handler{
		"cloud.archive.export":  c.export,
		"cloud.archive.import":  c.importArchive,
		"cloud.archive.inspect": c.inspect,
		"cloud.archive.plan":    c.plan,
		"cloud.archive.status":  c.status,
		"cloud.archive.cancel":  c.cancel,
	}
}

func (c *archiveControl) snapshot() any {
	if c.job == nil {
		return map[string]string{"state": "idle"}
	}
	job := *c.job
	job.Bytes = c.job.progress.Load()
	return &job
}

func (c *archiveControl) status(context.Context, json.RawMessage) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot(), nil
}

func (c *archiveControl) cancel(context.Context, json.RawMessage) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job != nil && c.job.State == "running" {
		c.job.cancel()
	}
	return c.snapshot(), nil
}

// start registers a job unless one is running, then runs work in the
// background.
func (c *archiveControl) start(kind string, work func(ctx context.Context, job *archiveJob) error) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job != nil && c.job.State == "running" {
		return nil, &control.Fault{Code: "busy", Message: "An export or import is already running"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &archiveJob{Kind: kind, State: "running", Started: time.Now().UTC(), cancel: cancel, progress: new(atomic.Int64)}
	c.job = job
	go func() {
		err := work(ctx, job)
		c.mu.Lock()
		defer c.mu.Unlock()
		now := time.Now().UTC()
		job.Finished = &now
		switch {
		case err == nil:
			job.State = "done"
		case ctx.Err() != nil:
			job.State = "cancelled"
		default:
			job.State, job.Error = "failed", err.Error()
		}
		cancel()
		log.Printf("cloud archive %s %s after %s (%d bytes): %v", kind, job.State, now.Sub(job.Started).Round(time.Second), job.progress.Load(), err)
	}()
	return c.snapshot(), nil
}

// open runs one fennec-terminal command on the cloud computer over the
// resident command socket.
func (c *archiveControl) open(ctx context.Context) (*commandstream.Session, error) {
	if _, _, _, ok := c.s.account.Session(); !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Fennec"}
	}
	c.s.mu.Lock()
	channel := c.s.channel
	c.s.mu.Unlock()
	if channel == nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if err = transport.Ready(ctx); err != nil {
		return nil, err
	}
	if !transport.Supports(terminalstream.CapabilityCommand) || !transport.Supports(terminalstream.CapabilityCloudArchive) {
		return nil, &control.Fault{Code: "not_ready", Message: "The cloud computer is updating", Retryable: true}
	}
	conn, err := transport.DialSocket(ctx, terminalstream.SocketCommand, "")
	if err != nil {
		return nil, err
	}
	return commandstream.NewStream(ctx, conn), nil
}

const archiveCommand = `"$HOME/.local/fennec/bin/fennec-terminal" archive `

// remoteError keeps the cloud's one-line reason, without the exit status.
func remoteError(err error, stderr *bytes.Buffer) error {
	var exit *commandstream.ExitError
	if errors.As(err, &exit) {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return errors.New(last)
		}
	}
	return err
}

// plan asks the cloud computer what an export would hold, with and without
// sessions, so the export dialog can show counts and sizes before it starts.
func (c *archiveControl) plan(ctx context.Context, _ json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second) // the apps wait 30 s
	defer cancel()
	session, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	if err := session.Run(archiveCommand + "plan"); err != nil {
		return nil, remoteError(err, &stderr)
	}
	var plan cloudarchive.Plan
	if json.Unmarshal(stdout.Bytes(), &plan) != nil || plan.All.Format != cloudarchive.Format {
		return nil, &control.Fault{Code: "not_ready", Message: "The cloud computer is updating", Retryable: true}
	}
	return plan, nil
}

func archiveDirectory(home string) string { return filepath.Join(home, ".config", "fennec", "exports") }

func (c *archiveControl) export(_ context.Context, raw json.RawMessage) (any, error) {
	request := struct {
		Sessions *bool `json:"sessions"`
	}{}
	if len(raw) > 0 && json.Unmarshal(raw, &request) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid export request"}
	}
	sessions := request.Sessions == nil || *request.Sessions
	return c.start("export", func(ctx context.Context, job *archiveJob) error {
		directory := archiveDirectory(c.s.home)
		if err := os.MkdirAll(directory, 0700); err != nil {
			return err
		}
		name := "fennec-" + time.Now().Format("20060102-150405") + cloudarchive.Extension
		partial := filepath.Join(directory, name+".partial")
		file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer os.Remove(partial)
		defer file.Close()
		session, err := c.open(ctx)
		if err != nil {
			return err
		}
		defer session.Close()
		var stderr bytes.Buffer
		session.Stdout = progressWriter{file, job.progress}
		session.Stderr = &manifestWriter{buffer: &stderr, found: func(m cloudarchive.Manifest) {
			c.mu.Lock()
			job.Manifest, job.Total = &m, m.Counts.Bytes
			c.mu.Unlock()
		}}
		stop := context.AfterFunc(ctx, func() { session.Close() })
		defer stop()
		command := archiveCommand + "export"
		if !sessions {
			command += " --no-sessions"
		}
		if err := session.Run(command); err != nil {
			return remoteError(err, &stderr)
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		// A complete archive is the only kind that gets the real name.
		if err := verifyFile(ctx, partial); err != nil {
			return fmt.Errorf("the exported archive is incomplete")
		}
		path := filepath.Join(directory, name)
		if err := os.Rename(partial, path); err != nil {
			return err
		}
		pruneArchives(directory, path)
		c.mu.Lock()
		job.Path, job.Name = path, name
		c.mu.Unlock()
		return nil
	})
}

func verifyFile(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = cloudarchive.Verify(ctx, file)
	return err
}

// pruneArchives keeps the new export and the newest one before it: archives
// are large, and the Settings page moves the new one where the user chooses.
func pruneArchives(directory, keep string) {
	matches, _ := filepath.Glob(filepath.Join(directory, "fennec-*"+cloudarchive.Extension))
	sort.Sort(sort.Reverse(sort.StringSlice(matches))) // names sort by time
	older := 0
	for _, match := range matches {
		if match == keep {
			continue
		}
		if older++; older > 1 {
			os.Remove(match)
		}
	}
}

// openArchive accepts only an absolute path to a regular file.
func openArchive(raw json.RawMessage) (*os.File, os.FileInfo, error) {
	var request struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &request) != nil || !filepath.IsAbs(request.Path) {
		return nil, nil, &control.Fault{Code: "invalid_params", Message: "Choose a Fennec archive file"}
	}
	file, err := os.Open(request.Path)
	if err != nil {
		return nil, nil, &control.Fault{Code: "not_found", Message: "The file could not be opened"}
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, &control.Fault{Code: "invalid_params", Message: "Choose a Fennec archive file"}
	}
	return file, info, nil
}

func (c *archiveControl) inspect(_ context.Context, raw json.RawMessage) (any, error) {
	file, _, err := openArchive(raw)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	manifest, err := cloudarchive.Inspect(file)
	if err != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: err.Error()}
	}
	return manifest, nil
}

func (c *archiveControl) importArchive(_ context.Context, raw json.RawMessage) (any, error) {
	file, info, err := openArchive(raw)
	if err != nil {
		return nil, err
	}
	manifest, err := cloudarchive.Inspect(file)
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		file.Close()
		return nil, &control.Fault{Code: "invalid_params", Message: err.Error()}
	}
	return c.start("import", func(ctx context.Context, job *archiveJob) error {
		defer file.Close()
		c.mu.Lock()
		job.Path, job.Name, job.Total, job.Manifest = file.Name(), filepath.Base(file.Name()), info.Size(), &manifest
		c.mu.Unlock()
		session, err := c.open(ctx)
		if err != nil {
			return err
		}
		defer session.Close()
		var stdout, stderr bytes.Buffer
		session.Stdin = progressReader{file, job.progress}
		session.Stdout, session.Stderr = &stdout, &stderr
		stop := context.AfterFunc(ctx, func() { session.Close() })
		defer stop()
		if err := session.Run(archiveCommand + "import"); err != nil {
			return remoteError(err, &stderr)
		}
		var result cloudarchive.Result
		if json.Unmarshal(stdout.Bytes(), &result) != nil {
			return fmt.Errorf("the cloud computer did not confirm the import")
		}
		c.mu.Lock()
		job.Result = &result
		c.mu.Unlock()
		c.rebindBoard()
		return nil
	})
}

// rebindBoard lets this Mac's queue replica join the cloud computer the user
// just imported into. The replica remembers the computer (or account) it
// first synced with in paired-account and refuses any other, so moving to a
// new computer would otherwise stop queue sync for good. Importing is the
// user's explicit choice of the new computer; replication writes the current
// identity again on its next step and merges what the Mac has.
func (c *archiveControl) rebindBoard() {
	pair := filepath.Join(c.s.home, ".fen/board/paired-account")
	if err := os.Remove(pair); err != nil && !os.IsNotExist(err) {
		log.Printf("cloud archive: could not rebind the queue replica: %v", err)
		return
	}
	_ = (board.Store{Home: c.s.home, Role: "local"}).SetSyncBlocked("")
	log.Printf("cloud archive: queue replica rebinds to the current cloud computer")
}

type progressWriter struct {
	w       io.Writer
	counter *atomic.Int64
}

func (p progressWriter) Write(data []byte) (int, error) {
	n, err := p.w.Write(data)
	p.counter.Add(int64(n))
	return n, err
}

type progressReader struct {
	r       io.Reader
	counter *atomic.Int64
}

func (p progressReader) Read(data []byte) (int, error) {
	n, err := p.r.Read(data)
	p.counter.Add(int64(n))
	return n, err
}

// manifestWriter collects stderr and reports the manifest line the export
// prints before its first file.
type manifestWriter struct {
	buffer *bytes.Buffer
	found  func(cloudarchive.Manifest)
	done   bool
}

func (m *manifestWriter) Write(data []byte) (int, error) {
	if m.buffer.Len() < 64<<10 {
		m.buffer.Write(data)
	}
	if !m.done {
		if line, _, ok := bytes.Cut(m.buffer.Bytes(), []byte("\n")); ok {
			m.done = true
			var manifest cloudarchive.Manifest
			if json.Unmarshal(line, &manifest) == nil && manifest.Format == cloudarchive.Format {
				m.found(manifest)
			}
		}
	}
	return len(data), nil
}
