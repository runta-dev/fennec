package launcher

import (
	"bytes"
	"context"
	"errors"
	"fennec/internal/terminalstream"
	"image"
	"image/png"
	"testing"
)

func TestClipboardCompressionRequiresPeerCapability(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 100, 80))); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		window := terminalstream.NewCreditWindow()
		window.Take(1)
		var sent bytes.Buffer
		err := sendClipboardImage(context.Background(), []byte{22}, 1, window, func(f terminalstream.Frame) error {
			sent.Write(f.Data)
			window.Add(len(f.Data))
			return nil
		}, func(context.Context) ([]byte, error) { return pngData.Bytes(), nil }, func() { t.Fatal("compression failed") }, nil, enabled)
		window.Close()
		if err != nil {
			t.Fatal(err)
		}
		_, format, err := image.DecodeConfig(bytes.NewReader(sent.Bytes()))
		want := "png"
		if enabled {
			want = "webp"
		}
		if err != nil || format != want {
			t.Fatalf("format %s, expected %s: %v", format, want, err)
		}
	}
}

func TestClipboardUploadCreditsAndTextFallback(t *testing.T) {
	for _, kind := range []string{"image", "text", "error"} {
		t.Run(kind, func(t *testing.T) {
			window := terminalstream.NewCreditWindow()
			window.Take(1)
			data := bytes.Repeat([]byte("x"), terminalstream.Window*3)
			var uploaded []byte
			noticed, absent, first := false, false, true
			err := sendClipboardImage(context.Background(), []byte{22}, 7, window, func(frame terminalstream.Frame) error {
				if kind == "text" && (frame.Type != "input" || string(frame.Data) != "\x16") {
					t.Fatal(frame)
				}
				if frame.Seq != 7 {
					t.Fatalf("frame without the shortcut's sequence number: %+v", frame.Seq)
				}
				if kind == "image" {
					if frame.Type != "image" || len(frame.Data) > terminalstream.Chunk {
						t.Fatal(frame)
					}
					if first && frame.Bytes != len(data) || !first && frame.Bytes != 0 {
						t.Fatal("wrong upload size", frame.Bytes)
					}
					first = false
				}
				uploaded = append(uploaded, frame.Data...)
				if !window.Add(len(frame.Data)) {
					t.Fatal("bad credit accounting")
				}
				return nil
			}, func(context.Context) ([]byte, error) {
				switch kind {
				case "image":
					return data, nil
				case "error":
					return nil, errors.New("unavailable")
				default:
					return nil, nil
				}
			}, func() { noticed = true }, func() { absent = true })
			if err != nil {
				t.Fatal(err)
			}
			if absent != (kind == "text") {
				t.Fatal("a predicted image must be withdrawn when the clipboard has none")
			}
			if kind == "image" && !bytes.Equal(uploaded, data) {
				t.Fatal("image changed")
			}
			if noticed != (kind == "error") {
				t.Fatal("wrong error handling")
			}
			if window.Take(terminalstream.Window) != terminalstream.Window {
				t.Fatal("input credits leaked")
			}
		})
	}
}
