package runtime

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestCollectPaintStopsWhenTheFirstPaintIsDone(t *testing.T) {
	reader, writer := io.Pipe()
	go func() {
		writer.Write([]byte("\x1b[H\x1b[2Jfirst"))
		writer.Write([]byte(" paint"))
		time.Sleep(200 * time.Millisecond)
		writer.Write([]byte("later"))
	}()
	paint, err := collectPaint(context.Background(), reader, 40*time.Millisecond, time.Second)
	if err != nil || string(paint) != "\x1b[H\x1b[2Jfirst paint" {
		t.Fatalf("paint = %q, %v", paint, err)
	}
}

func TestCollectPaintStopsWhileTheProgramKeepsDrawing(t *testing.T) {
	reader, writer := io.Pipe()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				writer.Write([]byte("x"))
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()
	started := time.Now()
	paint, err := collectPaint(context.Background(), reader, 40*time.Millisecond, 100*time.Millisecond)
	if err != nil || len(paint) == 0 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("paint = %d bytes after %s, %v", len(paint), time.Since(started), err)
	}
}

func TestCollectPaintReportsAWatcherThatEndedSilently(t *testing.T) {
	reader, writer := io.Pipe()
	writer.Close()
	if _, err := collectPaint(context.Background(), reader, 40*time.Millisecond, time.Second); err == nil {
		t.Fatal("an empty watch must fail so the plain read is used")
	}
}
