package board

import (
	"bytes"
	"encoding/base64"
	"image"
	pixelcolor "image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
)

func decodeThumbnail(t *testing.T, url string) image.Image {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, thumbnailPrefix))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// A wide screenshot of 1-px light text strokes on a dark background, like a
// terminal: the old 128 px nearest-neighbor thumbnail dropped or kept whole
// strokes at random, which looked like noise.
func screenshotFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1088, 502))
	for y := 0; y < 502; y++ {
		for x := 0; x < 1088; x++ {
			c := pixelcolor.NRGBA{30, 30, 36, 255}
			if x%2 == 0 {
				c = pixelcolor.NRGBA{230, 230, 230, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestThumbnailIsSharpSquareAndAveraged(t *testing.T) {
	s := testStore(t)
	a, err := s.SaveAttachment(screenshotFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Thumbnail) > 32<<10 || !currentThumbnail(a) {
		t.Fatalf("thumbnail is %d bytes, current=%v", len(a.Thumbnail), currentThumbnail(a))
	}
	img := decodeThumbnail(t, a.Thumbnail)
	if b := img.Bounds(); b.Dx() != ThumbnailSide || b.Dy() != ThumbnailSide {
		t.Fatalf("thumbnail is %v, want %d px square", b, ThumbnailSide)
	}
	// Alternating 1-px columns average to a mid gray instead of aliasing to
	// pure stripes of one color.
	for _, x := range []int{20, 21, 96, 97, 170} {
		r, _, _, _ := img.At(x, 96).RGBA()
		if v := r >> 8; v < 100 || v > 160 {
			t.Fatalf("pixel %d is %d, want an average near 130", x, v)
		}
	}
}

func TestSmallImagesKeepTheirSize(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 60, 40))
	url, err := thumbnail(img)
	if err != nil {
		t.Fatal(err)
	}
	if b := decodeThumbnail(t, url).Bounds(); b.Dx() != 40 || b.Dy() != 40 {
		t.Fatalf("thumbnail is %v, want 40 px square", b)
	}
	if !currentThumbnail(Attachment{Width: 60, Height: 40, Thumbnail: url}) {
		t.Fatal("a small image's thumbnail should be current")
	}
}

func TestOldThumbnailsAreRemadeForTheUI(t *testing.T) {
	s := testStore(t)
	a, err := s.SaveAttachment(screenshotFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	fresh := a.Thumbnail
	// What older versions stored: 128 px on the long side.
	var out bytes.Buffer
	if err := jpeg.Encode(&out, image.NewRGBA(image.Rect(0, 0, 128, 59)), nil); err != nil {
		t.Fatal(err)
	}
	old := a
	old.Thumbnail = thumbnailPrefix + base64.StdEncoding.EncodeToString(out.Bytes())
	if currentThumbnail(old) {
		t.Fatal("old thumbnail reported current")
	}
	if err := writeJSON(filepath.Join(s.attachmentDir(), a.ID+".json"), old); err != nil {
		t.Fatal(err)
	}
	r := Request{Messages: []Message{{Attachments: []Attachment{old}}}}
	got := s.RequestForUI(r)
	if got.Messages[0].Attachments[0].Thumbnail != fresh {
		t.Fatal("UI read kept the old thumbnail")
	}
	if r.Messages[0].Attachments[0].Thumbnail != old.Thumbnail {
		t.Fatal("RequestForUI changed the stored request")
	}
	saved, err := s.attachment(a.ID)
	if err != nil || saved.Thumbnail != fresh {
		t.Fatal("remade thumbnail was not saved with the attachment")
	}
	// Without the original, the old thumbnail is still better than nothing.
	missing := old
	missing.ID = strings.Repeat("0", 64)
	if s.RefreshThumbnail(missing) != old.Thumbnail {
		t.Fatal("missing original dropped the thumbnail")
	}
}
