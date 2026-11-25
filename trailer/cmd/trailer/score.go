package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	bpm  = 72.0
	beat = 60 / bpm
	bar  = 4 * beat
)

type chord struct {
	bass float64
	pad  []float64
	arp  []float64
}

var progression = []chord{
	{38, []float64{50, 54, 57, 61, 64}, []float64{62, 66, 69, 73, 76}}, // Dmaj9
	{35, []float64{47, 54, 57, 61, 62}, []float64{59, 62, 66, 69, 73}}, // Bm9
	{43, []float64{43, 50, 54, 57, 59}, []float64{55, 59, 62, 66, 69}}, // Gmaj9
	{45, []float64{45, 52, 57, 59, 62}, []float64{57, 62, 64, 69, 71}}, // Asus2/4
}

var arpPattern = []int{0, 2, 3, 4, 2, 3, 1, 2}

func (tl *Timeline) scene(id string) *Scene {
	for i := range tl.Scenes {
		if tl.Scenes[i].ID == id {
			return &tl.Scenes[i]
		}
	}
	return nil
}

func (sc *Scene) cue(name string) *Cue {
	for i := range sc.Cues {
		if sc.Cues[i].Name == name {
			return &sc.Cues[i]
		}
	}
	return nil
}

// sceneAt returns the scene playing at absolute time t.
func (tl *Timeline) sceneAt(t float64) *Scene {
	for i := range tl.Scenes {
		sc := &tl.Scenes[i]
		if t >= sc.Start && t < sc.Start+sc.Dur {
			return sc
		}
	}
	return &tl.Scenes[len(tl.Scenes)-1]
}

// music writes the score: a sparse, unresolved piano under the opening, a
// silence when the laptop closes, then a warm progression that starts with the
// logo and lands on a final chord with the end card.
func music(tl *Timeline, out *stereo) {
	lid, meet, end := tl.scene("lid"), tl.scene("meet"), tl.scene("end")

	// Opening: Bm9 / Gmaj9, two notes a beat, cut short by the lid.
	stop := lid.Start + lid.cue("lidClose").At + 0.15
	open := []struct{ low, hi []float64 }{
		{[]float64{47, 54}, []float64{66, 69}},
		{[]float64{47, 54}, []float64{69, 73}},
		{[]float64{43, 50}, []float64{66, 71}},
		{[]float64{43, 50}, []float64{62, 66}},
	}
	for b := 0; ; b++ {
		t0 := 0.5 + float64(b)*bar
		if t0 >= stop {
			break
		}
		o := open[b%len(open)]
		for _, n := range o.low {
			piano(out, t0, math.Min(bar, stop-t0), n, 0.42, -0.15)
		}
		for k, n := range o.hi {
			t := t0 + float64(2+k)*beat
			if t < stop-0.2 {
				piano(out, t, math.Min(beat*1.6, stop-t), n, 0.34, 0.25)
			}
		}
	}

	// Main progression, from the logo to the end card.
	gridStart := meet.Start + meet.cue("logo").At
	gridEnd := end.Start + end.cue("logo").At - 0.15
	bassFrom := tl.scene("cloud").Start
	sparkleFrom := tl.scene("terminal").Start
	for b := 0; ; b++ {
		t0 := gridStart + float64(b)*bar
		if t0 >= gridEnd-0.4 {
			break
		}
		c := progression[b%len(progression)]
		hold := math.Min(bar, gridEnd-t0)
		for k, n := range c.pad[1:] {
			pad(out, t0, hold, n, 0.4, -0.45+float64(k)*0.3)
		}
		if t0 >= bassFrom {
			bass(out, t0, hold, c.bass, 0.75)
		}
		for i, idx := range arpPattern {
			t := t0 + float64(i)*beat/2
			if t >= gridEnd-0.1 {
				break
			}
			vel := 0.36 + 0.1*float64((i+1)%2)
			if i == 0 {
				vel = 0.5
			}
			piano(out, t, math.Min(beat*1.5, gridEnd-t), c.arp[idx], vel, -0.3+0.6*float64(idx)/4)
		}
		if t0 >= sparkleFrom && b%2 == 0 {
			piano(out, t0+beat*3, beat*2, c.arp[4]+12, 0.22, 0.45)
		}
	}

	// Final chord with the end card.
	tf := gridEnd + 0.15
	for k, n := range []float64{50, 57, 61, 64, 66, 69} {
		piano(out, tf+float64(k)*0.045, 5.5, n, 0.55, -0.4+float64(k)*0.16)
	}
	for k, n := range progression[0].pad[1:] {
		pad(out, tf, 4.5, n, 0.45, -0.45+float64(k)*0.3)
	}
	bass(out, tf, 4.5, 38, 0.8)
	piano(out, tf+0.6, 4.5, 81, 0.3, 0.35)
	piano(out, tf+1.2, 4.0, 78, 0.24, -0.25)
}

// sfx places every cue's sound effect.
func sfx(tl *Timeline, out *stereo) {
	for _, sc := range tl.Scenes {
		for ci, c := range sc.Cues {
			at := sc.Start + c.At
			seed := int64(ci*7919) + int64(at*1000)
			switch c.SFX {
			case "lid":
				lidThump(out, at, 0.55, false)
			case "lidOpen":
				lidThump(out, at, 0.5, true)
			case "chime":
				bell(out, at, 86, 0.10, -0.2, 3.0)
				bell(out, at+0.09, 93, 0.07, 0.25, 2.6)
			case "chimeFinal":
				bell(out, at, 86, 0.09, -0.2, 3.5)
				bell(out, at+0.12, 90, 0.06, 0.1, 3.2)
				bell(out, at+0.24, 93, 0.06, 0.3, 3.4)
			case "whoosh":
				noiseSweep(out, at-0.35, 1.1, 350, 4200, 0.16, seed)
			case "whooshSoft":
				noiseSweep(out, at-0.2, 0.9, 500, 3000, 0.09, seed)
			case "typing":
				for i, r := range []rune(c.Text) {
					g := 0.22
					if r == ' ' {
						g = 0.3
					}
					keyClick(out, at+float64(i)/c.CPS, g, 0.1, seed+int64(i))
				}
				// Enter.
				keyClick(out, at+float64(len([]rune(c.Text)))/c.CPS+0.35, 0.34, 0.1, seed+99)
			case "keys":
				for i := 0; i < c.Count; i++ {
					keyClick(out, at+float64(i)*c.Interval, 0.32, 0.1, seed+int64(i))
				}
			case "pop":
				blip(out, at, 880, 0.08, 0.2)
			case "pops":
				for i := 0; i < c.Count; i++ {
					notes := []float64{74, 78, 81, 83, 86, 81, 78, 83, 86, 88, 90}
					blip(out, at+float64(i)*c.Interval, midiHz(notes[i%len(notes)]), 0.05, -0.6+1.2*float64(i)/float64(max(1, c.Count-1)))
				}
			case "click":
				blip(out, at, 1500, 0.10, 0.2)
				keyClick(out, at, 0.12, 0.2, seed)
			case "ding":
				bell(out, at, 88, 0.07, 0.4, 1.6)
				bell(out, at+0.13, 95, 0.06, 0.4, 1.8)
			case "":
			default:
				fmt.Fprintf(os.Stderr, "warning: unknown sfx %q\n", c.SFX)
			}
		}
	}
}

// decodeVO turns a recorded line into mono float samples at sampleRate.
func decodeVO(path string) ([]float32, error) {
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "f32le", "-ac", "1", "-ar", fmt.Sprint(sampleRate), "-")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	s := make([]float32, buf.Len()/4)
	if err := binary.Read(&buf, binary.LittleEndian, s); err != nil {
		return nil, err
	}
	return s, nil
}

// mixAudio renders the full soundtrack to a WAV file and reports how many
// lines had recorded voice-over.
func mixAudio(root string, tl *Timeline, path string) (int, error) {
	length := tl.Duration + 0.5
	mus := newStereo(length)
	music(tl, mus)
	fx := newStereo(length)
	sfx(tl, fx)

	// Duck the music under the voice.
	duck := make([]float32, mus.len())
	for i := range duck {
		duck[i] = 1
	}
	for _, sc := range tl.Scenes {
		for _, ln := range sc.Lines {
			a, b := sc.Start+ln.Start, sc.Start+ln.End
			for i := int((a - 0.3) * sampleRate); i < int((b+0.5)*sampleRate); i++ {
				if i < 0 || i >= len(duck) {
					continue
				}
				t := float64(i) / sampleRate
				d := math.Min(1, math.Min((t-(a-0.3))/0.3, ((b+0.5)-t)/0.5))
				g := float32(1 - 0.55*d)
				if g < duck[i] {
					duck[i] = g
				}
			}
		}
	}
	for i := range duck {
		mus.L[i] *= duck[i]
		mus.R[i] *= duck[i]
	}

	master := newStereo(length)
	master.mix(mus, 1)
	master.mix(reverb(mus, 0.85, 0.35, 1), 0.55)
	master.mix(fx, 1)
	master.mix(reverb(fx, 0.7, 0.4, 1), 0.35)

	vo := newStereo(length)
	recorded := 0
	for _, sc := range tl.Scenes {
		for _, ln := range sc.Lines {
			if !ln.Recorded {
				continue
			}
			s, err := decodeVO(filepath.Join(root, "assets", "vo", ln.ID+".mp3"))
			if err != nil {
				return 0, err
			}
			// Level each line to the same loudness.
			var sum float64
			for _, v := range s {
				sum += float64(v * v)
			}
			gain := 1.0
			if rms := math.Sqrt(sum / float64(len(s))); rms > 0 {
				gain = 0.12 / rms
			}
			at := int((sc.Start + ln.Start) * sampleRate)
			for i, v := range s {
				vo.add(at+i, float64(v)*gain, 0)
			}
			recorded++
		}
	}
	master.mix(vo, 1)
	master.mix(reverb(vo, 0.4, 0.5, 1), 0.08)

	// Fade out and soft-limit.
	fadeFrom := int((tl.Duration - 1.2) * sampleRate)
	peak := 0.0
	for i := range master.L {
		g := 1.0
		if i > fadeFrom {
			g = math.Max(0, 1-float64(i-fadeFrom)/(1.2*sampleRate))
		}
		for _, ch := range [][]float32{master.L, master.R} {
			x := float64(ch[i]) * g
			x = math.Tanh(x*1.1) / 1.1
			ch[i] = float32(x)
			peak = math.Max(peak, math.Abs(x))
		}
	}
	norm := 1.0
	if peak > 0.89 {
		norm = 0.89 / peak
	}
	fmt.Printf("audio: peak %.2f, output gain %.2f, %d voice lines\n", peak, norm, recorded)
	return recorded, writeWAV(path, master, norm)
}

func writeWAV(path string, s *stereo, gain float64) error {
	n := s.len()
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + n*4))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(2))
	w(uint32(sampleRate))
	w(uint32(sampleRate * 4))
	w(uint16(4))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(n * 4))
	pcm := make([]int16, 2*n)
	for i := 0; i < n; i++ {
		pcm[2*i] = int16(math.Max(-1, math.Min(1, float64(s.L[i])*gain)) * 32767)
		pcm[2*i+1] = int16(math.Max(-1, math.Min(1, float64(s.R[i])*gain)) * 32767)
	}
	w(pcm)
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// normalizeLoudness brings the mix to -16 LUFS with a two-pass, linear
// ffmpeg loudnorm, so the trailer plays at a consistent level online.
func normalizeLoudness(in, out string) error {
	measure := exec.Command("ffmpeg", "-hide_banner", "-nostats", "-i", in,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11:print_format=json", "-f", "null", "-")
	var stderr bytes.Buffer
	measure.Stderr = &stderr
	if err := measure.Run(); err != nil {
		return fmt.Errorf("measure loudness: %w", err)
	}
	s := stderr.String()
	i := strings.LastIndex(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return fmt.Errorf("measure loudness: no result")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s[i:j+1]), &m); err != nil {
		return err
	}
	af := fmt.Sprintf("loudnorm=I=-16:TP=-1.5:LRA=11:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true",
		m["input_i"], m["input_tp"], m["input_lra"], m["input_thresh"], m["target_offset"])
	apply := exec.Command("ffmpeg", "-y", "-v", "error", "-i", in, "-af", af, "-ar", fmt.Sprint(sampleRate), "-c:a", "pcm_s16le", out)
	apply.Stderr = os.Stderr
	if err := apply.Run(); err != nil {
		return fmt.Errorf("normalize loudness: %w", err)
	}
	fmt.Printf("audio: loudness %s LUFS -> -16 LUFS\n", m["input_i"])
	return nil
}
