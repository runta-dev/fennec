package terminalstream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fennec/internal/clipboard"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

func testImage(t *testing.T) []byte {
	t.Helper()
	bitmap := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range bitmap.Pix {
		bitmap.Pix[i] = byte(random.Uint32())
	}
	var data bytes.Buffer
	if err := png.Encode(&data, bitmap); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestImageUploadCompletePrivateAndPersistent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	data := testImage(t)
	var upload imageUpload
	for offset := 0; offset < len(data); {
		n := min(Chunk, len(data)-offset)
		frame := Frame{Data: data[offset : offset+n]}
		if offset == 0 {
			frame.Bytes = len(data)
		}
		path, err := upload.accept(frame)
		if err != nil {
			t.Fatal(err)
		}
		offset += n
		if offset < len(data) {
			if path != "" {
				t.Fatal("published a partial image")
			}
			continue
		}
		upload.reset()
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, data) {
			t.Fatal("attachment lost after upload", err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("attachment is not private", err)
		}
	}
}

func TestImageUploadRejectsInvalidAndInterruptedData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	for _, frames := range [][]Frame{
		{{Bytes: clipboard.MaxImageBytes + 1, Data: []byte("x")}},
		{{Bytes: 2, Data: []byte("not png")}},
		{{Bytes: 7, Data: []byte("not png")}},
		{{Data: []byte("x")}},
		{{Bytes: 3, Data: []byte("x")}, {Bytes: 3, Data: []byte("x")}},
	} {
		var upload imageUpload
		var err error
		for _, frame := range frames {
			_, err = upload.accept(frame)
		}
		if err == nil {
			t.Fatal("accepted invalid upload", frames)
		}
	}
	var upload imageUpload
	upload.accept(Frame{Bytes: 100, Data: []byte("partial")})
	upload.reset()
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("partial/invalid files leaked", files)
	}
}

func TestImagePasteChild(t *testing.T) {
	if os.Getenv("FENNEC_IMAGE_TEST_CHILD") != "1" {
		return
	}
	if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
		os.Exit(2)
	}
	fmt.Print("READY")
	reader := bufio.NewReader(os.Stdin)
	start, _ := reader.ReadString('~')
	end, _ := reader.ReadString('~')
	if start != "\x1b[200~" || !strings.HasSuffix(end, "\x1b[201~") {
		os.Exit(3)
	}
	path := strings.TrimSuffix(end, "\x1b[201~")
	data, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != os.Getenv("FENNEC_IMAGE_TEST_HASH") {
		os.Exit(4)
	}
	os.Remove(path)
	tail := make([]byte, len("after\r"))
	if _, err := io.ReadFull(reader, tail); err != nil || string(tail) != "after\r" {
		os.Exit(5)
	}
	fmt.Print("IMAGE_OK")
	os.Exit(0)
}

func TestImagePasteOverResidentTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data := testImage(t)
	if len(data) <= Window {
		t.Fatal("fixture must exercise backpressure")
	}
	input, send := io.Pipe()
	receive, output := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Serve(ctx, input, output, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			executable, err := os.Executable()
			command := exec.Command(executable, "-test.run=^TestImagePasteChild$")
			command.Env = append(os.Environ(), "FENNEC_IMAGE_TEST_CHILD=1", fmt.Sprintf("FENNEC_IMAGE_TEST_HASH=%x", sha256.Sum256(data)))
			return cloudruntime.LocalTerminal{ID: request.ID, Agent: request.Agent, Address: "t1", State: "live"}, command, err
		})
	}()
	transport := StartPipes(send, receive)
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports(CapabilityImagePaste) {
		t.Fatal("missing capability")
	}
	request := cloudruntime.LocalLaunch{ID: "image-bridge", Agent: "codex", LocalDirectory: "/project", Device: "1234abcd"}
	session, err := transport.Launch(Frame{Type: "launch", ID: request.ID, Launch: &request})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	window := NewCreditWindow()
	defer window.Close()
	stop := context.AfterFunc(ctx, window.Close)
	defer stop()
	sent := make(chan error, 1)
	var shown bytes.Buffer
	started := false
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch frame.Type {
		case "data":
			shown.Write(frame.Data)
			session.Send(Frame{Type: "credit", Bytes: len(frame.Data)})
			if !started && strings.Contains(shown.String(), "READY") {
				started = true
				go func() {
					remaining, total := data, len(data)
					for len(remaining) > 0 {
						n := window.Take(min(Chunk, len(remaining)))
						if n == 0 {
							sent <- ctx.Err()
							return
						}
						if err := session.Send(Frame{Type: "image", Data: remaining[:n], Bytes: total}); err != nil {
							sent <- err
							return
						}
						remaining, total = remaining[n:], 0
					}
					window.Take(len("after\r"))
					sent <- session.Send(Frame{Type: "input", Data: []byte("after\r")})
				}()
			}
		case "input_credit":
			if !window.Add(frame.Bytes) {
				t.Fatal("invalid credit", frame.Bytes)
			}
		case "error", "paste_error":
			t.Fatal(frame)
		case "exit":
			if frame.Code != 0 || !strings.Contains(shown.String(), "IMAGE_OK") {
				t.Fatal(frame.Code, shown.String())
			}
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			cancel()
			transport.Close()
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("server did not stop")
			}
			return
		}
	}
}

func TestLegacyKimiPasteDoesNotInsertPath(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	session := &serverSession{ctx: ctx, conn: writer, input: make(chan Frame, 64), imageAgent: "kimi", imageSession: "image-legacy"}
	events := make(chan Frame, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.pumpInput("image-legacy", func(frame Frame) error { events <- frame; return nil })
	}()
	defer func() { cancel(); writer.Close(); <-done }()
	data := testImage(t)
	total := len(data)
	for len(data) > 0 {
		n := min(Chunk, len(data))
		session.input <- Frame{Type: "image", Data: data[:n], Bytes: total}
		data, total = data[n:], 0
	}
	session.input <- Frame{Type: "input", Data: []byte("after")}
	reader.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "after" {
		t.Fatalf("legacy paste inserted content or blocked input: %q %v", got, err)
	}
	for {
		select {
		case frame := <-events:
			if frame.Type == "paste_error" {
				if frame.Message != "Start a new Kimi session to enable image paste" {
					t.Fatal(frame)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("missing actionable legacy notice")
		}
	}
}
