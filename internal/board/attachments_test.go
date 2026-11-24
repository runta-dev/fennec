package board

import (
	"bytes"
	"errors"
	"image"
	pixelcolor "image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func attachmentFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 384, 384))
	v := uint32(7)
	for y := 0; y < 384; y++ {
		for x := 0; x < 384; x++ {
			v ^= v << 13
			v ^= v >> 17
			v ^= v << 5
			img.SetNRGBA(x, y, pixelcolor.NRGBA{uint8(v), uint8(v >> 8), uint8(v >> 16), 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestRequestImagesPersistAndResumeAcrossReplicas(t *testing.T) {
	a, b := testStore(t), testStore(t)
	data := attachmentFixture(t)
	image, err := a.SaveAttachment(data)
	if err != nil {
		t.Fatal(err)
	}
	if image.Width != 384 || image.Height != 384 || image.Size != int64(len(data)) || !a.HasAttachment(image) {
		t.Fatal("invalid saved metadata")
	}
	again, err := a.SaveAttachment(data)
	if err != nil || again.ID != image.ID {
		t.Fatal("image import did not deduplicate")
	}
	p, _ := a.CreateProject(Project{Name: "Images"})
	request, err := a.CreateRequestWithAttachments(p.ID, "me", "Inspect this screenshot", "", "", []Attachment{image})
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	remote, _ := b.Request(p.ID, request.Number)
	if len(remote.Messages[0].Attachments) != 1 || b.AttachmentsReady(remote) {
		t.Fatal("metadata was treated as a complete image")
	}
	if _, err = b.Claim(p.ID, request.Number, "calm-fox", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("started without original image", err)
	}
	missing := b.MissingAttachments()
	if len(missing) != 1 || missing[0].Offset != 0 {
		t.Fatal("missing image was not requested")
	}
	first, err := a.ReadAttachmentChunk(image.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.WriteAttachmentChunk(first); err != nil {
		t.Fatal(err)
	}
	if err = b.WriteAttachmentChunk(first); err != nil {
		t.Fatal("lost upload reply was not replayable", err)
	}
	b = Store{Home: b.Home}
	missing = b.MissingAttachments()
	if len(missing) != 1 || missing[0].Offset != int64(len(first.Data)) {
		t.Fatal("partial image did not survive restart")
	}
	for offset := missing[0].Offset; offset < image.Size; {
		chunk, err := a.ReadAttachmentChunk(image.ID, offset)
		if err != nil {
			t.Fatal(err)
		}
		if err = b.WriteAttachmentChunk(chunk); err != nil {
			t.Fatal(err)
		}
		offset += int64(len(chunk.Data))
	}
	if !b.AttachmentsReady(remote) || len(b.MissingAttachments()) != 0 {
		t.Fatal("complete image remained pending")
	}
	actual, err := os.ReadFile(b.AttachmentPath(image))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("original pixels were changed during sync")
	}
	if _, err = b.Claim(p.ID, request.Number, "calm-fox", "codex"); err != nil {
		t.Fatal(err)
	}
}
func TestRequestImagesRejectInvalidAndCorruptedUploads(t *testing.T) {
	a, b := testStore(t), testStore(t)
	data := attachmentFixture(t)
	image, err := a.SaveAttachment(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SaveAttachment([]byte("not an image")); err == nil {
		t.Fatal("non-image accepted")
	}
	if _, err = a.SaveAttachment(make([]byte, MaxImageBytes+1)); err == nil {
		t.Fatal("oversized image accepted")
	}
	bad := image
	bad.ID = "../outside"
	if err = b.WriteAttachmentChunk(AttachmentChunk{Attachment: bad, Data: []byte("x")}); err == nil {
		t.Fatal("path traversal accepted")
	}
	for offset := int64(0); offset < image.Size; {
		chunk, _ := a.ReadAttachmentChunk(image.ID, offset)
		offset += int64(len(chunk.Data))
		if offset == image.Size {
			chunk.Data[len(chunk.Data)-1] ^= 1
			if err = b.WriteAttachmentChunk(chunk); err == nil {
				t.Fatal("corruption accepted")
			}
		} else if err = b.WriteAttachmentChunk(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if b.HasAttachment(image) {
		t.Fatal("corrupt original became visible")
	}
	if _, err = os.Stat(filepath.Join(b.attachmentDir(), image.ID+".part")); !os.IsNotExist(err) {
		t.Fatal("corrupt partial cannot restart")
	}
}

func TestImageInputRequiresResultConfirmationAndHidesThumbnailFromAgent(t *testing.T) {
	s := testStore(t)
	a, err := s.SaveAttachment(attachmentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(Project{Name: "Input"})
	r, err := s.CreateRequestWithAttachments(p.ID, "me", "Check image", "", "", []Attachment{a})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(p.ID, r.Number, "calm-fox", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Fail(p.ID, r.Number, "calm-fox"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reconcile(p.ID, r.Number, "calm-fox", "live"); !errors.Is(err, ErrConflict) {
		t.Fatal("live process confirmed ambiguous input", err)
	}
	r, err = s.Complete(p.ID, r.Number, "calm-fox", "codex", "Image inspected", Resolved)
	if err != nil {
		t.Fatal(err)
	}
	clean := RequestForAgent(r)
	if clean.Messages[0].Attachments[0].Thumbnail != "" || r.Messages[0].Attachments[0].Thumbnail == "" {
		t.Fatal("agent serialization changed stored thumbnail")
	}
}

func TestPartialRequestMetadataCannotStartBeforeImageReferences(t *testing.T) {
	a, b := testStore(t), testStore(t)
	image, err := a.SaveAttachment(attachmentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.CreateProject(Project{Name: "Partial"})
	request, err := a.CreateRequestWithAttachments(p.ID, "me", "Inspect image", "", "", []Attachment{image})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Sync(Exchange{})
	if err != nil {
		t.Fatal(err)
	}
	cut := -1
	for i, event := range out.Events {
		if event.Object == "projects/"+p.ID+"/requests/1.json" && event.Fields["messages"] != nil {
			cut = i
			break
		}
	}
	if cut < 0 {
		t.Fatal("message mutation missing")
	}
	if _, err = b.Sync(Exchange{Events: out.Events[:cut]}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(p.ID, request.Number, "calm-fox", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("partial request was started", err)
	}
	if _, err = b.Sync(Exchange{Events: out.Events[cut:]}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(p.ID, request.Number, "calm-fox", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("metadata alone started an image request", err)
	}
}
