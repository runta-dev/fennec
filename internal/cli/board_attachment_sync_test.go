package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	"image"
	"image/color"
	"image/png"
	"testing"
)

type attachmentSyncProbe struct {
	calls int
	sizes []int
}

func (p *attachmentSyncProbe) call(_ context.Context, _, _ string, body any) (json.RawMessage, error) {
	p.calls++
	p.sizes = append(p.sizes, len(body.(board.AttachmentChunk).Data))
	if p.calls == 1 {
		return nil, context.DeadlineExceeded
	}
	return json.RawMessage(`{}`), nil
}

func TestAttachmentSyncMakesBoundedProgressPastFailedUpload(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	im := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	var seed uint32 = 7
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			seed = seed*1664525 + 1013904223
			im.SetNRGBA(x, y, color.NRGBA{uint8(seed), uint8(seed >> 8), uint8(seed >> 16), 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		t.Fatal(err)
	}
	a, err := store.SaveAttachment(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	probe := &attachmentSyncProbe{}
	err = syncQueueAttachments(context.Background(), probe, store, []board.MissingAttachment{{Attachment: a}, {Attachment: a, Offset: 32 << 10}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || probe.calls != 2 {
		t.Fatalf("failed upload starved later images: %d %v", probe.calls, err)
	}
	for _, size := range probe.sizes {
		if size != 32<<10 {
			t.Fatalf("chunk too large: %d", size)
		}
	}
}
