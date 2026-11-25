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
	"strings"
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
	if a.Thumbnail, err = thumbnail(decoded); err != nil {
		return Attachment{}, err
	}
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

// ThumbnailSide is the pixel side of the square thumbnail: the desktop shows
// request images as 96 pt squares (object-fit cover) on 2x displays.
const ThumbnailSide = 192

const thumbnailPrefix = "data:image/jpeg;base64,"

// thumbnail is a center-cropped square JPEG data URL, area-averaged so text in
// screenshots stays legible, and small enough for validAttachment.
func thumbnail(src image.Image) (string, error) {
	b := src.Bounds()
	crop := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-crop)/2, b.Min.Y+(b.Dy()-crop)/2
	side := min(crop, ThumbnailSide)
	// Area-average: each output pixel is the exact coverage-weighted mean of
	// the source pixels under it, first across each row, then down columns.
	weights := areaWeights(crop, side)
	line := make([]float64, crop*3)
	rows := make([]float64, side*crop*3)
	for y := 0; y < crop; y++ {
		for x := 0; x < crop; x++ {
			// Composite on white, as before: JPEG has no alpha.
			v := pixelcolor.NRGBAModel.Convert(src.At(x0+x, y0+y)).(pixelcolor.NRGBA)
			a := float64(v.A) / 255
			line[x*3] = float64(v.R)*a + 255*(1-a)
			line[x*3+1] = float64(v.G)*a + 255*(1-a)
			line[x*3+2] = float64(v.B)*a + 255*(1-a)
		}
		for x, ws := range weights {
			for _, w := range ws {
				for c := 0; c < 3; c++ {
					rows[(y*side+x)*3+c] += line[w.index*3+c] * w.weight
				}
			}
		}
	}
	thumb := image.NewRGBA(image.Rect(0, 0, side, side))
	for y, ws := range weights {
		for x := 0; x < side; x++ {
			var rgb [3]float64
			for _, w := range ws {
				for c := 0; c < 3; c++ {
					rgb[c] += rows[(w.index*side+x)*3+c] * w.weight
				}
			}
			thumb.SetRGBA(x, y, pixelcolor.RGBA{uint8(min(255, rgb[0]+0.5)), uint8(min(255, rgb[1]+0.5)), uint8(min(255, rgb[2]+0.5)), 255})
		}
	}
	var url string
	for _, quality := range []int{90, 80, 70, 55, 40} {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, thumb, &jpeg.Options{Quality: quality}); err != nil {
			return "", err
		}
		url = thumbnailPrefix + base64.StdEncoding.EncodeToString(out.Bytes())
		if len(url) <= 32<<10 {
			return url, nil
		}
	}
	return "", ErrInvalid
}

type areaWeight struct {
	index  int
	weight float64
}

// areaWeights maps each of out pixels to the in pixels it covers, weighted by
// how much of each it covers; the weights of one output pixel sum to 1.
func areaWeights(in, out int) [][]areaWeight {
	scale := float64(in) / float64(out)
	weights := make([][]areaWeight, out)
	for o := range weights {
		start, end := float64(o)*scale, float64(o+1)*scale
		for i := int(start); i < in && float64(i) < end; i++ {
			cover := min(end, float64(i+1)) - max(start, float64(i))
			if cover > 0 {
				weights[o] = append(weights[o], areaWeight{i, cover / scale})
			}
		}
	}
	return weights
}

// currentThumbnail reports whether a stored thumbnail is the square kind
// thumbnail makes now. Older ones were 128 px on the long side, sampled
// nearest-neighbor, and looked blurry and broken when shown as a 96 pt square.
func currentThumbnail(a Attachment) bool {
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(a.Thumbnail, thumbnailPrefix))
	if err != nil || !strings.HasPrefix(a.Thumbnail, thumbnailPrefix) {
		return false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	return err == nil && cfg.Width == cfg.Height && cfg.Width == min(a.Width, a.Height, ThumbnailSide)
}

// RefreshThumbnail returns a's thumbnail, remade from the original when it was
// stored by an older version. The remade one is saved with the attachment's
// metadata; request files keep their copy so replication is unaffected.
func (s Store) RefreshThumbnail(a Attachment) string {
	if currentThumbnail(a) {
		return a.Thumbnail
	}
	saved, err := s.attachment(a.ID)
	if err != nil {
		return a.Thumbnail
	}
	if currentThumbnail(saved) {
		return saved.Thumbnail
	}
	data, err := os.ReadFile(s.AttachmentPath(saved))
	if err != nil {
		return a.Thumbnail
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return a.Thumbnail
	}
	if saved.Thumbnail, err = thumbnail(decoded); err != nil {
		return a.Thumbnail
	}
	_ = writeJSON(filepath.Join(s.attachmentDir(), saved.ID+".json"), saved)
	return saved.Thumbnail
}

// RequestForUI is r with every attachment's thumbnail brought up to date.
func (s Store) RequestForUI(r Request) Request {
	r.Messages = append([]Message(nil), r.Messages...)
	for i := range r.Messages {
		r.Messages[i].Attachments = append([]Attachment(nil), r.Messages[i].Attachments...)
		for j := range r.Messages[i].Attachments {
			a := &r.Messages[i].Attachments[j]
			a.Thumbnail = s.RefreshThumbnail(*a)
		}
	}
	return r
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
