// Package preview stores immutable, session-scoped artifacts. No resource ID
// grants network access: the HTTP handler is reachable only over an authenticated
// resident protocol socket, never a listening TCP port.
package preview

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"fennec/internal/sessionid"
)

const MaxBytes = 64 << 20

var RefPattern = regexp.MustCompile(`^pv_[a-f0-9]{32}$`)
var uploadPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrInvalid = errors.New("invalid preview request")
var ErrTooLarge = errors.New("attachment exceeds 64 MB")
var ErrConflict = errors.New("upload identifier already holds different content")

type Metadata struct {
	Ref       string    `json:"ref"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	MIME      string    `json:"mime"`
	Kind      string    `json:"kind"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}
type Upload struct {
	Path    string   `json:"path"`
	Preview Metadata `json:"preview"`
}
type Store struct{ Home string }

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && name != ".receipt.json" && len(name) <= 240 && !strings.ContainsAny(name, "/\\\x00\r\n\x1b")
}
func nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (s Store) workspace(session string) (*os.Root, error) {
	if !sessionid.Valid(session) {
		return nil, ErrInvalid
	}
	home, err := os.OpenRoot(s.Home)
	if err != nil {
		return nil, err
	}
	defer home.Close()
	workspaces, err := openDirectory(home, ".fen")
	if err != nil {
		return nil, err
	}
	defer workspaces.Close()
	return openDirectory(workspaces, session)
}

func openDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, ErrInvalid
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, ErrInvalid
	}
	return root, nil
}
func (s Store) registry(session string) (*os.Root, error) {
	if !sessionid.Valid(session) {
		return nil, ErrInvalid
	}
	home, err := os.OpenRoot(s.Home)
	if err != nil {
		return nil, err
	}
	defer home.Close()
	dir := filepath.Join(".local", "fennec", "previews", session)
	if err := home.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return home.OpenRoot(dir)
}

// Register resolves a source only inside the selected workspace and copies the
// opened file into private storage. os.Root prevents symlink/rename traversal.
func (s Store) Register(session, source, title string) (Metadata, error) {
	workspace, err := s.workspace(session)
	if err != nil {
		return Metadata{}, err
	}
	defer workspace.Close()
	if strings.HasPrefix(source, "~/") {
		source = filepath.Join(s.Home, source[2:])
	}
	if filepath.IsAbs(source) {
		source, err = filepath.Rel(filepath.Join(s.Home, ".fen", session), source)
		if err != nil {
			return Metadata{}, err
		}
	}
	if !filepath.IsLocal(source) {
		return Metadata{}, ErrInvalid
	}
	info, err := workspace.Stat(source)
	if err != nil {
		return Metadata{}, err
	}
	if !info.Mode().IsRegular() {
		return Metadata{}, ErrInvalid
	}
	file, err := workspace.OpenFile(source, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return Metadata{}, err
	}
	if !info.Mode().IsRegular() {
		return Metadata{}, ErrInvalid
	}
	if info.Size() > MaxBytes {
		return Metadata{}, ErrTooLarge
	}
	metadata, err := s.snapshot(session, filepath.Base(source), title, file)
	if err == nil && metadata.Size != info.Size() {
		return Metadata{}, errors.New("file changed during registration")
	}
	return metadata, err
}

func (s Store) snapshot(session, name, title string, source io.Reader) (Metadata, error) {
	if !validName(name) || len(title) > 512 || strings.ContainsAny(title, "\x00\r\n\x1b") {
		return Metadata{}, ErrInvalid
	}
	if title == "" {
		title = name
	}
	root, err := s.registry(session)
	if err != nil {
		return Metadata{}, err
	}
	defer root.Close()
	staging := ".pending-" + nonce()
	if err = root.Mkdir(staging, 0700); err != nil {
		return Metadata{}, err
	}
	defer root.RemoveAll(staging)
	file, err := root.OpenFile(filepath.Join(staging, "content"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, MaxBytes+1))
	if err != nil {
		return Metadata{}, err
	}
	if n > MaxBytes {
		return Metadata{}, ErrTooLarge
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, err
	}
	header := make([]byte, 512)
	count, readErr := file.Read(header)
	if readErr != nil && readErr != io.EOF {
		return Metadata{}, readErr
	}
	mime := http.DetectContentType(header[:count])
	identity := sha256.Sum256([]byte(session + "\x00" + name + "\x00" + title + "\x00" + digest))
	metadata := Metadata{Ref: "pv_" + hex.EncodeToString(identity[:16]), Name: name, Title: title, MIME: mime, Kind: mediaKind(mime, name), Size: n, SHA256: digest, CreatedAt: time.Now().UTC()}
	if err = file.Sync(); err != nil {
		return Metadata{}, err
	}
	if err = file.Close(); err != nil {
		return Metadata{}, err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return Metadata{}, err
	}
	if err = root.WriteFile(filepath.Join(staging, "metadata.json"), data, 0600); err != nil {
		return Metadata{}, err
	}
	if err = root.Rename(staging, metadata.Ref); err != nil {
		existing, readErr := s.Metadata(session, metadata.Ref)
		if readErr != nil || existing.SHA256 != digest {
			return Metadata{}, err
		}
		return existing, nil
	}
	return metadata, nil
}

func mediaKind(mime, name string) string {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return "image"
	case "application/pdf":
		return "document"
	}
	if strings.HasPrefix(mime, "audio/") {
		return "audio"
	}
	if strings.HasPrefix(mime, "video/") {
		return "video"
	}
	if strings.HasPrefix(mime, "text/plain") {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".txt", ".md", ".csv", ".json", ".log":
			return "document"
		}
	}
	// HTML, SVG and unknown content are downloadable, never embedded as active content.
	return "download"
}
func (s Store) Metadata(session, ref string) (Metadata, error) {
	if !RefPattern.MatchString(ref) {
		return Metadata{}, ErrInvalid
	}
	root, err := s.registry(session)
	if err != nil {
		return Metadata{}, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Join(ref, "metadata.json"))
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	var metadata Metadata
	err = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&metadata)
	if err != nil {
		return Metadata{}, err
	}
	if metadata.Ref != ref || !validName(metadata.Name) || metadata.Size < 0 || metadata.Size > MaxBytes {
		return Metadata{}, ErrInvalid
	}
	return metadata, nil
}
func (s Store) Open(session, ref string) (*os.File, Metadata, error) {
	metadata, err := s.Metadata(session, ref)
	if err != nil {
		return nil, Metadata{}, err
	}
	root, err := s.registry(session)
	if err != nil {
		return nil, Metadata{}, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Join(ref, "content"))
	if err != nil {
		return nil, Metadata{}, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != metadata.Size {
		file.Close()
		return nil, Metadata{}, ErrInvalid
	}
	return file, metadata, nil
}

// UploadFile is idempotent for an upload ID and its bytes/name. Retrying a lost
// response cannot overwrite another upload, or silently replace existing data.
func (s Store) UploadFile(session, id, name string, source io.Reader) (Upload, error) {
	if !uploadPattern.MatchString(id) || !validName(name) {
		return Upload{}, ErrInvalid
	}
	workspace, err := s.workspace(session)
	if err != nil {
		return Upload{}, err
	}
	defer workspace.Close()
	if err = workspace.MkdirAll("uploads", 0700); err != nil {
		return Upload{}, err
	}
	uploads, err := workspace.OpenRoot("uploads")
	if err != nil {
		return Upload{}, err
	}
	defer uploads.Close()
	staging := ".pending-" + nonce()
	if err = uploads.Mkdir(staging, 0700); err != nil {
		return Upload{}, err
	}
	defer uploads.RemoveAll(staging)
	file, err := uploads.OpenFile(filepath.Join(staging, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return Upload{}, err
	}
	defer file.Close()
	n, err := io.Copy(file, io.LimitReader(source, MaxBytes+1))
	if err != nil {
		return Upload{}, err
	}
	if n > MaxBytes {
		return Upload{}, ErrTooLarge
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return Upload{}, err
	}
	metadata, err := s.snapshot(session, name, "", file)
	if err != nil {
		return Upload{}, err
	}
	if err = file.Sync(); err != nil {
		return Upload{}, err
	}
	if err = file.Close(); err != nil {
		return Upload{}, err
	}
	result := Upload{Path: filepath.Join(s.Home, ".fen", session, "uploads", id, name), Preview: metadata}
	receipt, _ := json.Marshal(result)
	if err = uploads.WriteFile(filepath.Join(staging, ".receipt.json"), receipt, 0600); err != nil {
		return Upload{}, err
	}
	if err = uploads.Rename(staging, id); err != nil {
		old, readErr := uploads.ReadFile(filepath.Join(id, ".receipt.json"))
		var previous Upload
		if readErr != nil || json.Unmarshal(old, &previous) != nil || previous.Preview.Ref != metadata.Ref {
			return Upload{}, ErrConflict
		}
		return previous, nil
	}
	return result, nil
}

func Markup(ref, title string) string {
	title = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(title)
	return fmt.Sprintf(`![%s](fennec-preview:%s)`, title, ref)
}
