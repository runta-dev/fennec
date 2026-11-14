package launcher

import (
	"bytes"
	"context"
	"errors"
	"fennec/internal/terminalstream"
	"testing"
)

func TestClipboardUploadCreditsAndTextFallback(t *testing.T) {
	for _, kind := range []string{"image", "text", "error"} {
		t.Run(kind, func(t *testing.T) {
			window := terminalstream.NewCreditWindow()
			window.Take(1)
			data := bytes.Repeat([]byte("x"), terminalstream.Window*3)
			var uploaded []byte
			noticed, first := false, true
			err := sendClipboardImage(context.Background(), []byte{22}, window, func(frame terminalstream.Frame) error {
				if kind == "text" && (frame.Type != "input" || string(frame.Data) != "\x16") {
					t.Fatal(frame)
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
			}, func() { noticed = true })
			if err != nil {
				t.Fatal(err)
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
