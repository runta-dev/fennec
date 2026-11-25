package cloudarchive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
)

// ErrInvalid means the file is not a complete Fennec archive. Nothing was
// changed.
var ErrInvalid = errors.New("not a complete Fennec archive")

// Inspect reads only the manifest, for a preview before importing.
func Inspect(r io.Reader) (Manifest, error) {
	decoder, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return Manifest{}, ErrInvalid
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	header, err := reader.Next()
	if err != nil || header.Name != manifestName || header.Size > 1<<20 {
		return Manifest{}, ErrInvalid
	}
	var manifest Manifest
	if json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&manifest) != nil {
		return Manifest{}, ErrInvalid
	}
	return manifest, checkManifest(manifest)
}

func checkManifest(manifest Manifest) error {
	if manifest.Format != Format {
		return ErrInvalid
	}
	if manifest.Version != Version {
		return fmt.Errorf("this archive needs a newer version of Fennec (format %d)", manifest.Version)
	}
	return nil
}

// Import verifies the whole archive first, then merges it into home without
// overwriting anything already there. Importing the same archive again
// changes nothing.
func Import(ctx context.Context, home string, r io.Reader) (Result, error) {
	stagingRoot := filepath.Join(home, ".local/fennec/archive-import")
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		return Result{}, err
	}
	var suffix [8]byte
	rand.Read(suffix[:])
	staging := filepath.Join(stagingRoot, hex.EncodeToString(suffix[:]))
	defer os.RemoveAll(staging)
	manifest, trailer, err := extract(ctx, r, staging)
	if err != nil {
		return Result{}, err
	}
	result := Result{Manifest: manifest, Skipped: trailer.Skipped}
	if err := applyAttachments(home, staging, &result); err != nil {
		return result, err
	}
	if err := applyKnowledge(home, staging, &result); err != nil {
		return result, err
	}
	if err := applyBoard(ctx, home, staging, &result); err != nil {
		return result, err
	}
	if err := applySessions(ctx, home, staging, &result); err != nil {
		return result, err
	}
	return result, nil
}

// extract writes every entry into staging (or nowhere, when staging is "")
// and checks it against the trailer.
func extract(ctx context.Context, r io.Reader, staging string) (Manifest, Trailer, error) {
	decoder, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return Manifest{}, Trailer{}, ErrInvalid
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	var manifest Manifest
	var trailer *Trailer
	sums := map[string]string{}
	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			return Manifest{}, Trailer{}, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, Trailer{}, ErrInvalid
		}
		if trailer != nil || header.Typeflag != tar.TypeReg || !validName(header.Name) || sums[header.Name] != "" {
			return Manifest{}, Trailer{}, ErrInvalid
		}
		if first != (header.Name == manifestName) {
			return Manifest{}, Trailer{}, ErrInvalid
		}
		if header.Name == manifestName || header.Name == checksumsName {
			if header.Size > 256<<20 {
				return Manifest{}, Trailer{}, ErrInvalid
			}
			data, err := io.ReadAll(io.LimitReader(reader, header.Size))
			if err != nil || int64(len(data)) != header.Size {
				return Manifest{}, Trailer{}, ErrInvalid
			}
			if header.Name == manifestName {
				if json.Unmarshal(data, &manifest) != nil {
					return Manifest{}, Trailer{}, ErrInvalid
				}
				if err := checkManifest(manifest); err != nil {
					return Manifest{}, Trailer{}, err
				}
				sum := sha256.Sum256(data)
				sums[header.Name] = hex.EncodeToString(sum[:])
				continue
			}
			trailer = &Trailer{}
			if json.Unmarshal(data, trailer) != nil {
				return Manifest{}, Trailer{}, ErrInvalid
			}
			continue
		}
		sum := sha256.New()
		if staging == "" {
			// Verify only.
			written, err := io.Copy(sum, reader)
			if err != nil || written != header.Size {
				return Manifest{}, Trailer{}, ErrInvalid
			}
			sums[header.Name] = hex.EncodeToString(sum.Sum(nil))
			continue
		}
		destination := filepath.Join(staging, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return Manifest{}, Trailer{}, err
		}
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode).Perm()|0600)
		if err != nil {
			return Manifest{}, Trailer{}, err
		}
		written, err := io.Copy(io.MultiWriter(file, sum), reader)
		closeErr := file.Close()
		if err != nil || written != header.Size {
			return Manifest{}, Trailer{}, ErrInvalid
		}
		if closeErr != nil {
			return Manifest{}, Trailer{}, closeErr
		}
		os.Chtimes(destination, header.ModTime, header.ModTime)
		sums[header.Name] = hex.EncodeToString(sum.Sum(nil))
	}
	if trailer == nil || len(trailer.Files) != len(sums) {
		return Manifest{}, Trailer{}, ErrInvalid
	}
	for name, sum := range sums {
		if trailer.Files[name] != sum {
			return Manifest{}, Trailer{}, ErrInvalid
		}
	}
	return manifest, *trailer, nil
}

// validName accepts only the archive's own relative layout.
func validName(name string) bool {
	if name == manifestName || name == checksumsName || name == journalName {
		return true
	}
	if path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || strings.ContainsAny(name, "\x00\\") {
		return false
	}
	parts := strings.Split(name, "/")
	switch {
	case len(parts) == 3 && parts[0] == "board" && parts[1] == "attachments":
		return attachmentID.MatchString(parts[2])
	case len(parts) >= 4 && parts[0] == "board" && parts[1] == "knowledge":
		return board.ValidProjectID(parts[2])
	case len(parts) >= 3 && parts[0] == "sessions" && sessionID.MatchString(parts[1]):
		return parts[2] == "launch.json" && len(parts) == 3 || parts[2] == "workdir" && len(parts) >= 4 || parts[2] == "conversations" && len(parts) >= 5
	}
	return false
}

func applyAttachments(home, staging string, result *Result) error {
	source := filepath.Join(staging, "board", "attachments")
	entries, _ := os.ReadDir(source)
	target := filepath.Join(home, ".fen", "board", "attachments")
	store := board.Store{Home: home, Role: "cloud"}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		// An attachment is named by its content.
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != strings.SplitN(entry.Name(), ".", 2)[0] {
			result.Skipped = append(result.Skipped, Skip{"board/attachments/" + entry.Name(), "content does not match its name"})
			continue
		}
		_, imageErr := os.Lstat(filepath.Join(target, entry.Name()))
		_, metaErr := os.Lstat(filepath.Join(target, hex.EncodeToString(sum[:])+".json"))
		if imageErr == nil && metaErr == nil {
			continue
		}
		// SaveAttachment checks the image and writes its metadata and thumbnail.
		if _, err := store.SaveAttachment(data); err != nil {
			result.Skipped = append(result.Skipped, Skip{"board/attachments/" + entry.Name(), "not a valid image"})
			continue
		}
		result.AttachmentsAdded++
	}
	return nil
}

// applyKnowledge adds missing files. A file that exists with other content
// keeps its content; the imported one is saved next to it as
// <name>.imported<ext>.
func applyKnowledge(home, staging string, result *Result) error {
	source := filepath.Join(staging, "board", "knowledge")
	store := board.Store{Home: home, Role: "cloud"}
	return filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(source, file)
		parts := strings.SplitN(filepath.ToSlash(relative), "/", 2)
		if len(parts) != 2 || !board.ValidProjectID(parts[0]) {
			return nil
		}
		root := store.KnowledgePath(parts[0])
		destination := filepath.Join(root, filepath.FromSlash(parts[1]))
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := insideWithoutSymlinks(filepath.Join(home, ".fen"), destination); err != nil {
			return err
		}
		existing, err := os.ReadFile(destination)
		switch {
		case err == nil && bytes.Equal(existing, data):
			result.KnowledgeUnchanged++
			return nil
		case err == nil:
			ext := filepath.Ext(destination)
			destination = strings.TrimSuffix(destination, ext) + ".imported" + ext
			if previous, err := os.ReadFile(destination); err == nil && bytes.Equal(previous, data) {
				return nil
			}
			result.KnowledgeConflicts = append(result.KnowledgeConflicts, parts[0]+"/"+parts[1])
			return writeReplace(destination, data)
		case errors.Is(err, os.ErrNotExist):
			result.KnowledgeAdded++
			return writeNew(destination, data, 0600)
		default:
			return err
		}
	})
}

// applyBoard merges the journal through the board's own replication merge:
// fields already newer on this computer win, and a repeated import is a no-op.
func applyBoard(ctx context.Context, home, staging string, result *Result) error {
	data, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(journalName)))
	if err != nil {
		return ErrInvalid
	}
	var events []board.Mutation
	if json.Unmarshal(data, &events) != nil {
		return ErrInvalid
	}
	store := board.Store{Home: home, Role: "cloud"}
	before, err := requestKeys(store)
	if err != nil {
		return err
	}
	// Sync skips an origin's events at or below the clock it has seen, so each
	// origin's events must arrive in clock order across batches.
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Origin != events[j].Origin {
			return events[i].Origin < events[j].Origin
		}
		return events[i].Clock < events[j].Clock
	})
	seen := map[string]uint64{}
	for _, event := range events {
		if event.Clock > seen[event.Origin] {
			seen[event.Origin] = event.Clock
		}
	}
	for start := 0; start < len(events); start += board.SyncBatch {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+board.SyncBatch, len(events))
		if _, err := store.Sync(board.Exchange{Seen: seen, Events: events[start:end]}); err != nil {
			return fmt.Errorf("merge queues: %w", err)
		}
	}
	after, err := requestKeys(store)
	if err != nil {
		return err
	}
	result.EventsApplied = len(events)
	for key := range after {
		if before[key] {
			continue
		}
		if strings.HasSuffix(key, "/") {
			result.ProjectsAdded++
		} else {
			result.RequestsAdded++
		}
	}
	for key := range before {
		if !strings.HasSuffix(key, "/") && after[key] {
			result.RequestsExisting++
		}
	}
	projects, _ := store.Projects()
	for _, project := range projects {
		store.MirrorThreads(project.ID)
	}
	return nil
}

// requestKeys names every project ("id/") and request ("id/n").
func requestKeys(store board.Store) (map[string]bool, error) {
	keys := map[string]bool{}
	projects, err := store.Projects()
	if err != nil && !errors.Is(err, board.ErrNotFound) {
		return nil, err
	}
	for _, project := range projects {
		keys[project.ID+"/"] = true
		requests, err := store.Requests(project.ID)
		if err != nil {
			return nil, err
		}
		for _, request := range requests {
			keys[fmt.Sprintf("%s/%d", project.ID, request.Number)] = true
		}
	}
	return keys, nil
}

// applySessions restores sessions this computer does not have. A session
// folder that already exists is left alone; its conversations are still
// offered to the agent store, which keeps a newer copy.
func applySessions(ctx context.Context, home, staging string, result *Result) error {
	source := filepath.Join(staging, "sessions")
	entries, _ := os.ReadDir(source)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := entry.Name()
		if !sessionID.MatchString(id) {
			continue
		}
		dir := filepath.Join(source, id)
		if data, err := os.ReadFile(filepath.Join(dir, "launch.json")); err == nil {
			destination := filepath.Join(home, ".local/fennec/launches", id+".json")
			if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
				if err := writeNew(destination, data, 0600); err != nil {
					return err
				}
				result.LaunchesRestored++
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "workdir")); err == nil {
			workdir := filepath.Join(home, ".fen", id)
			if _, err := os.Lstat(workdir); err == nil {
				result.SessionsExisting++
			} else if errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(workdir), 0700); err != nil {
					return err
				}
				if err := moveTree(filepath.Join(dir, "workdir"), workdir); err != nil {
					return fmt.Errorf("restore session %s: %w", id, err)
				}
				result.SessionsRestored++
			} else {
				return err
			}
		}
		conversations, _ := os.ReadDir(filepath.Join(dir, "conversations"))
		for _, conversation := range conversations {
			bundle, err := readBundle(filepath.Join(dir, "conversations", conversation.Name()))
			if err == nil && bundle.LaunchID != id {
				err = ErrInvalid
			}
			if err == nil {
				var written int
				if written, err = cloudruntime.ApplySessionImport(home, bundle); err == nil && written > 0 {
					err = cloudruntime.RestoreSessionTime(home, bundle)
				}
			}
			if err != nil {
				result.ConversationsFailed++
				result.Skipped = append(result.Skipped, Skip{"sessions/" + id + "/conversations/" + conversation.Name(), err.Error()})
				continue
			}
			result.ConversationsCopied++
		}
	}
	return nil
}

func readBundle(dir string) (cloudruntime.SessionImport, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return cloudruntime.SessionImport{}, ErrInvalid
	}
	var header bundleHeader
	if json.Unmarshal(raw, &header) != nil {
		return cloudruntime.SessionImport{}, ErrInvalid
	}
	bundle := header.SessionImport
	bundle.Modified = header.Modified
	bundle.Files = nil
	for _, name := range header.Paths {
		if path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") || name == ".." {
			return cloudruntime.SessionImport{}, ErrInvalid
		}
		data, err := os.ReadFile(filepath.Join(dir, "files", filepath.FromSlash(name)))
		if err != nil {
			return cloudruntime.SessionImport{}, ErrInvalid
		}
		bundle.Files = append(bundle.Files, cloudruntime.SessionFile{Path: name, Data: data})
	}
	return bundle, cloudruntime.ValidateSessionImport(bundle)
}

// moveTree renames a staged folder into place, publishing it whole; across
// file systems it copies into a hidden sibling first.
func moveTree(source, destination string) error {
	err := os.Rename(source, destination)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	temporary := filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".importing")
	os.RemoveAll(temporary)
	err = filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, file)
		target := filepath.Join(temporary, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
	if err != nil {
		os.RemoveAll(temporary)
		return err
	}
	return os.Rename(temporary, destination)
}

// insideWithoutSymlinks refuses a destination outside root or below a
// symlink, so an archive cannot write through a link on this computer.
func insideWithoutSymlinks(root, destination string) error {
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalid
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", current)
		}
	}
	return nil
}

func writeNew(destination string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(destination)
		return err
	}
	return file.Close()
}

func writeReplace(destination string, data []byte) error {
	if info, err := os.Lstat(destination); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", destination)
	}
	temporary := destination + ".tmp"
	os.Remove(temporary)
	if err := writeNew(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}

// Verify reads the whole archive and checks every entry against its
// checksums without writing anything.
func Verify(ctx context.Context, r io.Reader) (Manifest, error) {
	manifest, _, err := extract(ctx, r, "")
	return manifest, err
}
