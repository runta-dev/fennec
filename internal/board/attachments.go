package board

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	pixelcolor "image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// Match herdr-core terminal.put, the existing terminal image-paste transport.
const MaxImageBytes = 8 << 20
const AttachmentChunkBytes = 256 << 10
const MaxAttachments = 8

var attachmentID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Attachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Thumbnail string `json:"thumbnail"`
}
type MissingAttachment struct {
	Attachment Attachment `json:"attachment"`
	Offset     int64      `json:"offset"`
}
type AttachmentChunk struct {
	Attachment Attachment `json:"attachment"`
	Offset     int64      `json:"offset"`
	Data       []byte     `json:"data"`
}

func validAttachment(a Attachment) bool {
	return attachmentID.MatchString(a.ID) && a.Size > 0 && a.Size <= MaxImageBytes && (a.MediaType == "image/png" || a.MediaType == "image/jpeg") && a.Width > 0 && a.Height > 0 && a.Width <= 16384 && a.Height <= 16384 && int64(a.Width)*int64(a.Height) <= 32_000_000 && len(a.Thumbnail) <= 32<<10
}
func (s Store) attachmentDir() string { return filepath.Join(s.root(), "attachments") }
func (s Store) AttachmentPath(a Attachment) string {
	ext := ".png"
	if a.MediaType == "image/jpeg" {
		ext = ".jpg"
	}
	return filepath.Join(s.attachmentDir(), a.ID+ext)
}
func (s Store) HasAttachment(a Attachment) bool {
	if !validAttachment(a) {
		return false
	}
	info, err := os.Lstat(s.AttachmentPath(a))
	if err != nil || !info.Mode().IsRegular() || info.Size() != a.Size {
		return false
	}
	var saved Attachment
	return readJSON(filepath.Join(s.attachmentDir(), a.ID+".json"), &saved) == nil && saved.ID == a.ID && saved.Size == a.Size && saved.Width == a.Width && saved.Height == a.Height && saved.MediaType == a.MediaType
}
func (s Store) ImportAttachment(path string) (Attachment, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Attachment{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Attachment{}, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return Attachment{}, err
	}
	return s.SaveAttachment(data)
}
func (s Store) SaveAttachment(data []byte) (Attachment, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return Attachment{}, ErrInvalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 16384 || cfg.Height > 16384 || int64(cfg.Width)*int64(cfg.Height) > 32_000_000 {
		return Attachment{}, ErrInvalid
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	a := Attachment{ID: id, Name: "image." + format, MediaType: "image/" + format, Size: int64(len(data)), Width: cfg.Width, Height: cfg.Height}
	if format == "jpeg" {
		a.Name = "image.jpg"
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Attachment{}, ErrInvalid
	}
	width, height := cfg.Width, cfg.Height
	scale := max(width, height)
	if scale > 128 {
		width = max(1, width*128/scale)
		height = max(1, height*128/scale)
	}
	thumb := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := pixelcolor.NRGBAModel.Convert(decoded.At(decoded.Bounds().Min.X+x*cfg.Width/width, decoded.Bounds().Min.Y+y*cfg.Height/height)).(pixelcolor.NRGBA)
			alpha := uint32(v.A)
			thumb.SetRGBA(x, y, pixelcolor.RGBA{uint8((uint32(v.R)*alpha + 255*(255-alpha)) / 255), uint8((uint32(v.G)*alpha + 255*(255-alpha)) / 255), uint8((uint32(v.B)*alpha + 255*(255-alpha)) / 255), 255})
		}
	}
	var out bytes.Buffer
	if err = jpeg.Encode(&out, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return Attachment{}, err
	}
	a.Thumbnail = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
	if !validAttachment(a) {
		return Attachment{}, ErrInvalid
	}
	if err = os.MkdirAll(s.attachmentDir(), 0700); err != nil {
		return Attachment{}, err
	}
	file, err := os.CreateTemp(s.attachmentDir(), ".image-*")
	if err != nil {
		return Attachment{}, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return Attachment{}, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return Attachment{}, err
	}
	if err = file.Close(); err != nil {
		return Attachment{}, err
	}
	if err = os.Rename(file.Name(), s.AttachmentPath(a)); err != nil {
		return Attachment{}, err
	}
	if err = writeJSON(filepath.Join(s.attachmentDir(), id+".json"), a); err != nil {
		return Attachment{}, err
	}
	return a, nil
}
func (s Store) attachment(id string) (Attachment, error) {
	var a Attachment
	if !attachmentID.MatchString(id) {
		return a, ErrInvalid
	}
	err := readJSON(filepath.Join(s.attachmentDir(), id+".json"), &a)
	if err == nil && (!validAttachment(a) || a.ID != id || !s.HasAttachment(a)) {
		err = ErrInvalid
	}
	return a, err
}

func (s Store) AttachmentURL(id string) (string, error) {
	a, err := s.attachment(id)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: s.AttachmentPath(a)}).String(), nil
}
func (s Store) validateAttachments(images []Attachment) ([]Attachment, error) {
	if len(images) > MaxAttachments {
		return nil, ErrInvalid
	}
	out := []Attachment{}
	seen := map[string]bool{}
	for _, image := range images {
		if seen[image.ID] {
			continue
		}
		a, err := s.attachment(image.ID)
		if err != nil {
			return nil, err
		}
		seen[image.ID] = true
		out = append(out, a)
	}
	return out, nil
}
func RequestAttachments(r Request) []Attachment {
	out := []Attachment{}
	seen := map[string]bool{}
	for _, m := range r.Messages {
		for _, a := range m.Attachments {
			if !seen[a.ID] {
				out = append(out, a)
				seen[a.ID] = true
			}
		}
	}
	return out
}

// Agent reads keep attachment metadata, but never dump UI thumbnail base64
// into the model's text context. Originals reach the TUI through herdr-core.
func RequestForAgent(r Request) Request {
	r.Messages = append([]Message(nil), r.Messages...)
	for i := range r.Messages {
		r.Messages[i].Attachments = append([]Attachment(nil), r.Messages[i].Attachments...)
		for j := range r.Messages[i].Attachments {
			r.Messages[i].Attachments[j].Thumbnail = ""
		}
	}
	return r
}
func (s Store) AttachmentsReady(r Request) bool {
	for _, a := range RequestAttachments(r) {
		if !s.HasAttachment(a) {
			return false
		}
	}
	return true
}
func (s Store) MissingAttachments() []MissingAttachment {
	out := []MissingAttachment{}
	seen := map[string]bool{}
	projects, _ := s.Projects()
	for _, p := range projects {
		list, _ := s.Requests(p.ID)
		for _, sum := range list {
			r, err := s.Request(p.ID, sum.Number)
			if err != nil {
				continue
			}
			for _, a := range RequestAttachments(r) {
				if !validAttachment(a) || seen[a.ID] || s.HasAttachment(a) {
					continue
				}
				seen[a.ID] = true
				offset := int64(0)
				if info, err := os.Lstat(filepath.Join(s.attachmentDir(), a.ID+".part")); err == nil && info.Mode().IsRegular() && info.Size() < a.Size {
					offset = info.Size()
				}
				out = append(out, MissingAttachment{a, offset})
				if len(out) >= 32 {
					return out
				}
			}
		}
	}
	return out
}
func (s Store) ReadAttachmentChunk(id string, offset int64) (AttachmentChunk, error) {
	a, err := s.attachment(id)
	if err != nil {
		return AttachmentChunk{}, err
	}
	if offset < 0 || offset >= a.Size {
		return AttachmentChunk{}, ErrInvalid
	}
	f, err := os.Open(s.AttachmentPath(a))
	if err != nil {
		return AttachmentChunk{}, err
	}
	defer f.Close()
	data := make([]byte, min(int64(AttachmentChunkBytes), a.Size-offset))
	_, err = f.ReadAt(data, offset)
	return AttachmentChunk{a, offset, data}, err
}
func (s Store) WriteAttachmentChunk(chunk AttachmentChunk) error {
	a := chunk.Attachment
	if !validAttachment(a) || len(chunk.Data) == 0 || len(chunk.Data) > AttachmentChunkBytes || chunk.Offset < 0 || chunk.Offset >= a.Size || chunk.Offset+int64(len(chunk.Data)) > a.Size {
		return ErrInvalid
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if s.HasAttachment(a) {
		return nil
	}
	if err = os.MkdirAll(s.attachmentDir(), 0700); err != nil {
		return err
	}
	path := filepath.Join(s.attachmentDir(), a.ID+".part")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return ErrInvalid
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if chunk.Offset < info.Size() {
		old := make([]byte, len(chunk.Data))
		if _, err = file.ReadAt(old, chunk.Offset); err != nil || !bytes.Equal(old, chunk.Data) {
			return ErrConflict
		}
		if info.Size() != a.Size {
			return nil
		}
	} else {
		if chunk.Offset != info.Size() {
			return ErrConflict
		}
		if _, err = file.WriteAt(chunk.Data, chunk.Offset); err != nil {
			return err
		}
		if err = file.Sync(); err != nil {
			return err
		}
		if chunk.Offset+int64(len(chunk.Data)) != a.Size {
			return nil
		}
	}
	file.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.ID {
		os.Remove(path)
		return ErrInvalid
	}
	saved, err := s.SaveAttachment(data)
	if err != nil {
		return err
	}
	if saved.Width != a.Width || saved.Height != a.Height || saved.MediaType != a.MediaType {
		return ErrInvalid
	}
	return os.Remove(path)
}
