package cloudarchive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Options choose what an export carries.
type Options struct {
	// Sessions adds session folders, resume records and conversations.
	Sessions bool
	Now      func() time.Time
	// Planned receives the manifest before any file is written, so a caller
	// can show progress against Counts.Bytes.
	Planned func(Manifest)
	// PlanOnly stops after Planned: nothing is written (the Settings page
	// shows what an export would hold before the user starts it).
	PlanOnly bool
}

type plannedFile struct {
	source string
	name   string
	size   int64
}

type plannedSession struct {
	id      string
	agent   string
	launch  string // launch record path, or ""
	workdir string // working directory, or ""
	files   []plannedFile
}

// Export writes the archive of home's queues (and sessions) to w.
func Export(ctx context.Context, home string, options Options, w io.Writer) (Manifest, Trailer, error) {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	store := board.Store{Home: home, Role: "cloud"}
	events, err := store.Journal()
	if err != nil {
		return Manifest{}, Trailer{}, fmt.Errorf("read queues: %w", err)
	}
	journal, err := json.Marshal(events)
	if err != nil {
		return Manifest{}, Trailer{}, err
	}
	manifest := Manifest{Format: Format, Version: Version, Created: now().UTC(), SourceHome: home, Sessions: options.Sessions}
	manifest.Counts.Events = len(events)
	manifest.Counts.Bytes = int64(len(journal))
	projects, err := store.Projects()
	if err != nil {
		return Manifest{}, Trailer{}, fmt.Errorf("read queues: %w", err)
	}
	manifest.Counts.Projects = len(projects)
	for _, project := range projects {
		requests, err := store.Requests(project.ID)
		if err != nil {
			return Manifest{}, Trailer{}, fmt.Errorf("read queue %s: %w", project.ID, err)
		}
		manifest.Counts.Requests += len(requests)
	}
	var trailer Trailer
	skip := func(name, reason string) { trailer.Skipped = append(trailer.Skipped, Skip{name, reason}) }

	boardRoot := filepath.Join(home, ".fen", "board")
	var files []plannedFile
	entries, _ := os.ReadDir(filepath.Join(boardRoot, "attachments"))
	for _, entry := range entries {
		if !attachmentID.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, plannedFile{filepath.Join(boardRoot, "attachments", entry.Name()), "board/attachments/" + entry.Name(), info.Size()})
			manifest.Counts.Attachments++
		}
	}
	projectDirs, _ := os.ReadDir(filepath.Join(boardRoot, "projects"))
	for _, dir := range projectDirs {
		if !dir.IsDir() || !board.ValidProjectID(dir.Name()) {
			continue
		}
		root := store.KnowledgePath(dir.Name())
		planned := planTree(root, "board/knowledge/"+dir.Name(), nil, skip)
		manifest.Counts.Knowledge += len(planned)
		files = append(files, planned...)
	}
	for _, file := range files {
		manifest.Counts.Bytes += file.size
	}

	var sessions []plannedSession
	if options.Sessions {
		sessions = planSessions(home, skip)
		for _, session := range sessions {
			if session.launch != "" {
				manifest.Counts.Launches++
			}
			if session.workdir != "" {
				manifest.Counts.Sessions++
			}
			for _, file := range session.files {
				manifest.Counts.Bytes += file.size
			}
		}
	}

	if options.Planned != nil {
		options.Planned(manifest)
	}
	if options.PlanOnly {
		return manifest, trailer, nil
	}
	encoder, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return Manifest{}, Trailer{}, err
	}
	writer := &archiveWriter{tar: tar.NewWriter(encoder), sums: map[string]string{}, now: manifest.Created}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := writer.bytes(manifestName, raw); err != nil {
		return Manifest{}, Trailer{}, err
	}
	if err := writer.bytes(journalName, journal); err != nil {
		return Manifest{}, Trailer{}, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, Trailer{}, err
		}
		if err := writer.file(file, skip); err != nil {
			return Manifest{}, Trailer{}, err
		}
	}
	var codex map[string][]cloudruntime.CodexConversation
	if options.Sessions {
		codex = cloudruntime.CodexDirectories(home)
	}
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return Manifest{}, Trailer{}, err
		}
		prefix := "sessions/" + session.id + "/"
		if session.launch != "" {
			if err := writer.file(plannedFile{session.launch, prefix + "launch.json", -1}, skip); err != nil {
				return Manifest{}, Trailer{}, err
			}
		}
		for _, file := range session.files {
			if err := writer.file(file, skip); err != nil {
				return Manifest{}, Trailer{}, err
			}
		}
		if session.workdir == "" || session.agent == "" {
			continue
		}
		bundles, failures := cloudruntime.CollectArchiveSessions(home, session.agent, session.workdir, session.id, codex[filepath.Clean(session.workdir)])
		for _, failure := range failures {
			skip(prefix+"conversations", failure.Error())
		}
		for n, bundle := range bundles {
			if err := writer.conversation(prefix+"conversations/"+strconv.Itoa(n)+"/", bundle); err != nil {
				return Manifest{}, Trailer{}, err
			}
			trailer.Conversations++
		}
	}
	trailer.Files = writer.sums
	raw, _ = json.MarshalIndent(trailer, "", "  ")
	if err := writer.tar.WriteHeader(&tar.Header{Name: checksumsName, Mode: 0600, Size: int64(len(raw)), ModTime: manifest.Created, Typeflag: tar.TypeReg}); err != nil {
		return Manifest{}, Trailer{}, err
	}
	if _, err := writer.tar.Write(raw); err != nil {
		return Manifest{}, Trailer{}, err
	}
	if err := writer.tar.Close(); err != nil {
		return Manifest{}, Trailer{}, err
	}
	if err := encoder.Close(); err != nil {
		return Manifest{}, Trailer{}, err
	}
	return manifest, trailer, nil
}

// planSessions lists every session with a working directory or a resume
// record, sorted by ID.
func planSessions(home string, skip func(string, string)) []plannedSession {
	byID := map[string]*plannedSession{}
	get := func(id string) *plannedSession {
		if byID[id] == nil {
			byID[id] = &plannedSession{id: id}
		}
		return byID[id]
	}
	launches := filepath.Join(home, ".local/fennec/launches")
	entries, _ := os.ReadDir(launches)
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !sessionID.MatchString(id) || !entry.Type().IsRegular() {
			continue
		}
		file := filepath.Join(launches, entry.Name())
		var record struct {
			Agent string `json:"agent"`
		}
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &record) != nil {
			skip("sessions/"+id+"/launch.json", "unreadable resume record")
			continue
		}
		session := get(id)
		session.launch, session.agent = file, record.Agent
	}
	workdirs, _ := os.ReadDir(filepath.Join(home, ".fen"))
	for _, entry := range workdirs {
		if !entry.IsDir() || !sessionID.MatchString(entry.Name()) {
			continue
		}
		session := get(entry.Name())
		session.workdir = filepath.Join(home, ".fen", entry.Name())
		session.files = planTree(session.workdir, "sessions/"+entry.Name()+"/workdir", excludedDirectories, skip)
	}
	sessions := make([]plannedSession, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, *session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].id < sessions[j].id })
	return sessions
}

// planTree lists the regular files under root. Symlinks and special files
// are never followed or carried.
func planTree(root, prefix string, excluded map[string]bool, skip func(string, string)) []plannedFile {
	var files []plannedFile
	filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, _ := filepath.Rel(root, file)
		name := prefix + "/" + filepath.ToSlash(relative)
		if entry.IsDir() {
			if file != root && excluded[entry.Name()] {
				skip(name+"/", "code repository, dependencies or build output")
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFile {
			skip(name, "larger than 1 GB")
			return nil
		}
		files = append(files, plannedFile{file, name, info.Size()})
		return nil
	})
	return files
}

type archiveWriter struct {
	tar  *tar.Writer
	sums map[string]string
	now  time.Time
}

func (w *archiveWriter) begin(name string, size int64, mode fs.FileMode, modified time.Time) (hash.Hash, error) {
	if modified.IsZero() {
		modified = w.now
	}
	if err := w.tar.WriteHeader(&tar.Header{Name: name, Mode: int64(mode.Perm() | 0600), Size: size, ModTime: modified, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return nil, err
	}
	return sha256.New(), nil
}

func (w *archiveWriter) bytes(name string, data []byte) error {
	sum, err := w.begin(name, int64(len(data)), 0600, time.Time{})
	if err != nil {
		return err
	}
	sum.Write(data)
	if _, err := w.tar.Write(data); err != nil {
		return err
	}
	w.sums[name] = hex.EncodeToString(sum.Sum(nil))
	return nil
}

// file copies one file. A file that shrinks while it is copied is padded and
// reported; one that disappears is skipped.
func (w *archiveWriter) file(file plannedFile, skip func(string, string)) error {
	handle, err := os.Open(file.source)
	if err != nil {
		skip(file.name, "removed while exporting")
		return nil
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() {
		skip(file.name, "not a regular file")
		return nil
	}
	sum, err := w.begin(file.name, info.Size(), info.Mode(), info.ModTime())
	if err != nil {
		return err
	}
	written, err := io.CopyN(io.MultiWriter(w.tar, sum), handle, info.Size())
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("export %s: %w", file.name, err)
	}
	if written < info.Size() {
		padding := make([]byte, info.Size()-written)
		w.tar.Write(padding)
		sum.Write(padding)
		skip(file.name, "changed while exporting")
	}
	w.sums[file.name] = hex.EncodeToString(sum.Sum(nil))
	return nil
}

// bundleHeader is a conversation without its file contents.
type bundleHeader struct {
	cloudruntime.SessionImport
	Paths    []string  `json:"paths"`
	Modified time.Time `json:"modified,omitempty"`
}

func (w *archiveWriter) conversation(prefix string, bundle *cloudruntime.SessionImport) error {
	header := bundleHeader{SessionImport: *bundle, Modified: bundle.Modified}
	header.Files = nil
	for _, file := range bundle.Files {
		header.Paths = append(header.Paths, file.Path)
	}
	raw, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if err := w.bytes(prefix+"bundle.json", raw); err != nil {
		return err
	}
	for _, file := range bundle.Files {
		if err := w.bytes(prefix+"files/"+path.Clean(file.Path), file.Data); err != nil {
			return err
		}
	}
	return nil
}
