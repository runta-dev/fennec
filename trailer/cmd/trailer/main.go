// Command trailer renders the Fennec trailer from code: canvas scenes in
// web/, music and sound effects synthesized in Go, and voice-over recorded
// with ElevenLabs. See ../../README.md.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	root, err := findRoot()
	if err != nil {
		fail(err)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "vo":
		fs := flag.NewFlagSet("vo", flag.ExitOnError)
		force := fs.Bool("force", false, "re-record even if the script is unchanged")
		fs.Parse(args)
		err = recordVO(root, *force)
	case "timeline":
		err = printTimeline(root)
	case "audio":
		fs := flag.NewFlagSet("audio", flag.ExitOnError)
		out := fs.String("o", filepath.Join(root, "out", "audio.wav"), "output WAV")
		stems := fs.String("stems", "", "also write bed.wav and voice.wav into this directory")
		fs.Parse(args)
		err = buildAudio(root, *out, *stems)
	case "still":
		fs := flag.NewFlagSet("still", flag.ExitOnError)
		captions := fs.Bool("captions", false, "draw the voice-over as captions")
		fs.Parse(args)
		err = stills(root, fs.Args(), *captions)
	case "serve":
		err = servePreview(root)
	case "render":
		fs := flag.NewFlagSet("render", flag.ExitOnError)
		out := fs.String("o", filepath.Join(root, "out", "fennec-trailer.mp4"), "output video")
		captions := fs.Bool("captions", false, "draw the voice-over as captions")
		workers := fs.Int("workers", max(1, min(6, runtime.NumCPU()/2)), "parallel browser pages")
		fs.Parse(args)
		err = render(root, *out, *captions, *workers)
	default:
		usage()
	}
	if err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: go run ./cmd/trailer <command>
  vo [-force]              record the voice-over with ElevenLabs (needs ELEVENLABS_API_KEY)
  timeline                 print the resolved timeline
  audio [-o f.wav] [-stems dir]
                           render the soundtrack only (stems: bed and voice)
  still [-captions] t...   render stills at the given seconds into out/stills
  serve                    live preview in a browser
  render [-captions] [-o file.mp4] [-workers n]
                           render the full trailer`)
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "trailer:", err)
	os.Exit(1)
}

func findRoot() (string, error) {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "timeline.json")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "web", "trailer.js")); err == nil {
				return d, nil
			}
		}
		if t := filepath.Join(d, "trailer"); fileExists(filepath.Join(t, "timeline.json")) {
			return t, nil
		}
		if filepath.Dir(d) == d {
			return "", errors.New("run inside the trailer directory")
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func resolved(root string, captions bool) (*Timeline, error) {
	tl, err := loadTimeline(root)
	if err != nil {
		return nil, err
	}
	if err := tl.resolve(root); err != nil {
		return nil, err
	}
	tl.Captions = captions
	return tl, nil
}

func printTimeline(root string) error {
	tl, err := resolved(root, false)
	if err != nil {
		return err
	}
	for _, sc := range tl.Scenes {
		fmt.Printf("%6.2f  %-9s %5.2fs\n", sc.Start, sc.ID, sc.Dur)
		fmt.Printf("        %6.2f-%6.2f %s\n", sc.Start+sc.SegStart, sc.Start+sc.SegEnd, sc.Text)
		for _, c := range sc.Cues {
			fmt.Printf("        %6.2f cue %s (%s)\n", sc.Start+c.At, c.Name, c.SFX)
		}
	}
	state := "estimated"
	if tl.Recorded {
		state = "recorded"
	}
	fmt.Printf("total %.2fs, voice-over %s, groove from %.2fs\n", tl.Duration, state, tl.Grid0)
	return nil
}

func buildAudio(root, out, stems string) error {
	tl, err := resolved(root, false)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	raw := strings.TrimSuffix(out, ".wav") + "-raw.wav"
	if err := mixAudio(root, tl, raw, stems); err != nil {
		return err
	}
	if err := normalizeLoudness(raw, out); err != nil {
		return err
	}
	os.Remove(raw)
	fmt.Println("wrote", out)
	return nil
}

// serve exposes web/ and the resolved timeline to the browser.
func serve(root string, tl *Timeline) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/timeline.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tl)
	})
	mux.Handle("/", http.FileServer(http.Dir(filepath.Join(root, "web"))))
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String() + "/index.html?render=1", func() { srv.Close() }, nil
}

func openPage(url string, tl *Timeline) (*browser, error) {
	b, err := launchBrowser(tl.Width, tl.Height)
	if err != nil {
		return nil, err
	}
	if err := b.call("Page.navigate", map[string]any{"url": url}, nil); err != nil {
		b.close()
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var ready bool
		b.eval("window.trailerReady === true", &ready)
		if ready {
			return b, nil
		}
		if time.Now().After(deadline) {
			var msg string
			b.eval("String(window.trailerError || 'page did not become ready')", &msg)
			b.close()
			return nil, errors.New(msg)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func grab(b *browser, frame int) ([]byte, error) {
	var url string
	if err := b.eval(fmt.Sprintf("renderFrame(%d)", frame), &url); err != nil {
		return nil, err
	}
	i := strings.IndexByte(url, ',')
	if i < 0 {
		return nil, fmt.Errorf("frame %d: no image", frame)
	}
	return base64.StdEncoding.DecodeString(url[i+1:])
}

func stills(root string, times []string, captions bool) error {
	tl, err := resolved(root, captions)
	if err != nil {
		return err
	}
	url, stop, err := serve(root, tl)
	if err != nil {
		return err
	}
	defer stop()
	b, err := openPage(url, tl)
	if err != nil {
		return err
	}
	defer b.close()
	dir := filepath.Join(root, "out", "stills")
	os.MkdirAll(dir, 0o755)
	if len(times) == 0 {
		for _, sc := range tl.Scenes {
			times = append(times, strconv.FormatFloat(sc.Start+sc.Dur*0.7, 'f', 2, 64))
		}
	}
	for _, s := range times {
		t, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		png, err := grab(b, int(t*float64(tl.FPS)))
		if err != nil {
			return err
		}
		p := filepath.Join(dir, fmt.Sprintf("t%06.2f.png", t))
		if err := os.WriteFile(p, png, 0o644); err != nil {
			return err
		}
		fmt.Println(p)
	}
	return nil
}

func render(root, out string, captions bool, workers int) error {
	tl, err := resolved(root, captions)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	wav := strings.TrimSuffix(out, filepath.Ext(out)) + ".wav"
	raw := strings.TrimSuffix(wav, ".wav") + "-raw.wav"
	if err := mixAudio(root, tl, raw, ""); err != nil {
		return err
	}
	if err := normalizeLoudness(raw, wav); err != nil {
		return err
	}
	os.Remove(raw)
	if !tl.Recorded {
		fmt.Println("note: no voice-over for this script yet; run `vo` to record it")
	}

	url, stop, err := serve(root, tl)
	if err != nil {
		return err
	}
	defer stop()

	total := int(tl.Duration * float64(tl.FPS))
	ff := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "image2pipe", "-framerate", strconv.Itoa(tl.FPS), "-c:v", "png", "-i", "-",
		"-i", wav,
		"-c:v", "libx264", "-preset", "slow", "-crf", "16", "-tune", "animation", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "256k", "-shortest", "-movflags", "+faststart", out)
	pipe, err := ff.StdinPipe()
	if err != nil {
		return err
	}
	ff.Stderr = os.Stderr
	if err := ff.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	type result struct {
		i   int
		png []byte
		err error
	}
	results := make(chan result, workers*4)
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		b, err := openPage(url, tl)
		if err != nil {
			pipe.Close()
			ff.Wait()
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer b.close()
			for i := range next {
				png, err := grab(b, i)
				results <- result{i, png, err}
			}
		}()
	}
	// Feed frames, but never run more than a window ahead of the writer.
	window := make(chan struct{}, workers*8)
	go func() {
		for i := 0; i < total; i++ {
			window <- struct{}{}
			next <- i
		}
		close(next)
		wg.Wait()
		close(results)
	}()

	started := time.Now()
	pending := map[int][]byte{}
	want := 0
	var firstErr error
	for r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		pending[r.i] = r.png
		for {
			png, ok := pending[want]
			if !ok {
				break
			}
			delete(pending, want)
			if firstErr == nil {
				if _, err := pipe.Write(png); err != nil {
					firstErr = err
				}
			}
			<-window
			want++
			if want%(tl.FPS*5) == 0 || want == total {
				el := time.Since(started).Seconds()
				fmt.Printf("\rframes %d/%d  %.1f fps", want, total, float64(want)/el)
			}
		}
	}
	fmt.Println()
	pipe.Close()
	if err := ff.Wait(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		return firstErr
	}
	fmt.Printf("wrote %s (%.1fs, %d frames)\n", out, tl.Duration, total)
	return nil
}

// servePreview plays the trailer in a browser in real time (no sound).
func servePreview(root string) error {
	tl, err := resolved(root, false)
	if err != nil {
		return err
	}
	url, _, err := serve(root, tl)
	if err != nil {
		return err
	}
	fmt.Println("preview:", strings.TrimSuffix(url, "?render=1"))
	fmt.Println("add ?captions for captions; click the picture to restart; Ctrl-C to stop")
	select {}
}
