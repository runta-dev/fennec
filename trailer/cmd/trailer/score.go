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

type chord struct {
	root float64   // sub bass note
	pad  []float64 // stab voicing
	arp  []float64 // four arpeggio notes
}

// Am7 - Fmaj7 - C - G, one chord per bar.
var progression = []chord{
	{33, []float64{57, 60, 64, 67}, []float64{69, 72, 76, 79}},
	{29, []float64{57, 60, 64, 65}, []float64{65, 69, 72, 76}},
	{36, []float64{55, 60, 64, 67}, []float64{67, 72, 76, 79}},
	{31, []float64{55, 59, 62, 67}, []float64{67, 71, 74, 79}},
}

var arpPattern = []int{0, 2, 1, 3, 2, 0, 3, 1}

// agentNames is shown in the "agents" scene, one per eighth note. The
// canvas keeps its own list (AGENT_NAMES); keep the counts equal.
const agentNames = 8

func posMod(a, b int) int { return ((a % b) + b) % b }

// buses separate the parts the kick side-chains from the parts it doesn't.
type buses struct {
	drums, bass, synth, fx *stereo
	kicks                  []float64
}

// music writes the score. Intro: a ticking clock, a heartbeat and a filtered
// arpeggio under the problem, cut dead when the agent stops. Drop: a 120 BPM
// groove from "Not anymore" to the end card. End: one hit and a ringing chord.
func music(tl *Timeline, b *buses) {
	beat := tl.Beat
	bar := 4 * beat
	d := tl.Grid0
	stopSc := tl.scene("stops")
	stop := stopSc.Start + stopSc.cue("stop").At
	endSc := tl.scene("end")
	end := endSc.Start + endSc.cue("logo").At
	barOf := func(t float64) int { return int(math.Floor((t - d + 1e-6) / bar)) }

	// Intro.
	for t := tl.gridAfter(0.15, beat/2, 0); t < stop-0.02; t += beat / 2 {
		i := int(math.Round((t - d) / (beat / 2)))
		if posMod(i, 2) == 0 {
			hat(b.drums, t, 0.55, 0.012, 0.3, int64(i))
		}
		if posMod(i, 8) == 0 {
			kick(b.drums, t, 0.35)
		}
		c := progression[posMod(barOf(t), 4)]
		pluck(b.synth, t, c.arp[arpPattern[posMod(i, 8)]]-12, 0.55, -0.2+0.4*float64(posMod(i, 4))/3, 700, 0.14)
	}

	// Groove, in sixteenth notes.
	q := beat / 4
	for i := 0; ; i++ {
		t := d + float64(i)*q
		if t >= end-0.03 {
			break
		}
		br := i / 16
		step := i % 16
		c := progression[br%4]
		progress := math.Min(1, (t-d)/math.Max(1, end-d))
		if step%4 == 0 {
			kick(b.drums, t, 1)
			b.kicks = append(b.kicks, t)
		}
		if br >= 1 && (step == 4 || step == 12) {
			clap(b.drums, t, 0.9, int64(i))
		}
		if step%4 == 2 {
			hat(b.drums, t, 0.7, 0.05, 0.25, int64(i)+7)
		} else if br >= 2 && step%2 == 1 {
			hat(b.drums, t, 0.35, 0.015, -0.25, int64(i)+11)
		}
		if step == 0 {
			bass(b.bass, t, bar-0.05, c.root+12, 1.6)
			bass(b.bass, t, bar-0.05, c.root, 1.2)
			for k, n := range c.pad {
				pad(b.synth, t, bar*0.9, n, 0.32, -0.45+float64(k)*0.3)
			}
		}
		if step%2 == 0 || br >= 1 {
			n := c.arp[arpPattern[i%8]]
			cut := 900 + 3200*progress
			pluck(b.synth, t, n, 0.6+0.25*b2f(step%4 == 0), -0.5+float64(i%4)*0.33, cut, 0.12)
		}
	}
	crash(b.fx, d, 0.8, 3)
	riser(b.fx, end, 1.8, 0.9, 5)

	// End card: a single hit and a ringing Cadd9.
	for k, n := range []float64{48, 55, 60, 62, 64, 67, 74} {
		pad(b.synth, end, 3.2, n, 0.3, -0.5+float64(k)*0.16)
	}
	bass(b.bass, end, 3.0, 36, 1.4)
	for k, n := range []float64{72, 76, 79, 84, 86} {
		pluck(b.synth, end+float64(k)*beat/2, n, 0.5, -0.4+float64(k)*0.2, 2400, 0.5)
	}
}

// sfx places the cue sounds and the scene-driven ticks and checkmarks.
func sfx(tl *Timeline, out *stereo) {
	for _, sc := range tl.Scenes {
		for ci, c := range sc.Cues {
			at := sc.Start + c.At
			seed := int64(ci*7919) + int64(at*1000)
			switch c.SFX {
			case "lid":
				lidThump(out, at, 0.7, false)
			case "powerDown":
				powerDown(out, at, 0.8)
			case "impact":
				impact(out, at, 0.75, seed)
			case "impactEnd":
				impact(out, at, 0.7, seed)
				bell(out, at+0.02, 86, 0.07, -0.2, 2.6)
				bell(out, at+0.1, 93, 0.05, 0.25, 2.4)
			case "whoosh":
				noiseSweep(out, at-0.3, 0.8, 400, 5000, 0.16, seed)
			case "whooshUp":
				noiseSweep(out, at-0.25, 0.6, 600, 6000, 0.12, seed)
			case "whooshDown":
				noiseSweep(out, at-0.1, 0.9, 5000, 300, 0.14, seed)
			case "typing":
				for i := range []rune(c.Text) {
					keyClick(out, at+float64(i)/c.CPS, 0.3, 0.1, seed+int64(i))
				}
				keyClick(out, at+float64(len([]rune(c.Text)))/c.CPS+0.18, 0.42, 0.1, seed+99)
			case "keys":
				for i := 0; i < c.Count; i++ {
					keyClick(out, at+float64(i)*c.Interval, 0.5, 0.1, seed+int64(i))
				}
			case "":
			default:
				fmt.Fprintf(os.Stderr, "warning: unknown sfx %q\n", c.SFX)
			}
		}
	}

	// One tick per agent name. Must match SCENES.agents in web/trailer.js.
	if sc := tl.scene("agents"); sc != nil {
		stopAt := sc.Start + sc.word("whatever").Start
		for k := 1; k < agentNames; k++ {
			t := tl.gridAfter(sc.Start+0.001, tl.Beat/2, k-1)
			if t >= stopAt {
				break
			}
			blip(out, t, midiHz(81+float64(k%4)*2), 0.06, -0.4+0.1*float64(k))
		}
	}
	// One chime per checked task. Must match SCENES.working.
	if sc := tl.scene("working"); sc != nil {
		for i := 0; i < 3; i++ {
			t := tl.gridAfter(sc.Start+0.25, tl.Beat, i)
			if t < sc.Start+sc.Dur {
				bell(out, t, 88+float64(i)*3, 0.04, 0.3, 0.8)
			}
		}
	}
}

// decodeVO turns the recorded take into mono float samples at sampleRate.
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

// mixAudio renders the soundtrack to a WAV file.
func mixAudio(root string, tl *Timeline, path, stems string) error {
	length := tl.Duration + 0.5
	b := &buses{drums: newStereo(length), bass: newStereo(length), synth: newStereo(length), fx: newStereo(length)}
	music(tl, b)
	sfx(tl, b.fx)

	// Kick side-chain on bass and synths.
	sc := make([]float32, b.bass.len())
	for i := range sc {
		sc[i] = 1
	}
	for _, k := range b.kicks {
		for i := int(k * sampleRate); i < int((k+0.45)*sampleRate) && i < len(sc); i++ {
			t := float64(i)/sampleRate - k
			g := float32(1 - 0.6*math.Exp(-t/0.11))
			if g < sc[i] {
				sc[i] = g
			}
		}
	}

	// Voice-over, one continuous take.
	vo := newStereo(length)
	env := make([]float32, vo.len())
	if tl.Recorded {
		s, err := decodeVO(filepath.Join(root, "assets", "vo", "take.mp3"))
		if err != nil {
			return err
		}
		var sum float64
		var n int
		for _, v := range s {
			if math.Abs(float64(v)) > 0.01 {
				sum += float64(v * v)
				n++
			}
		}
		gain := 1.0
		if n > 0 {
			gain = 0.22 / math.Sqrt(sum/float64(n))
		}
		// Place the take, opening the pauses at the split points. Each cut
		// sits between sentences and gets a short fade so it doesn't click.
		const fade = 0.008
		e := 0.0
		for i, v := range s {
			tau := float64(i) / sampleRate
			shift := 0.0
			g := 1.0
			for _, sp := range tl.splits {
				if tau >= sp.At {
					shift += sp.Shift
				}
				if d := math.Abs(tau - sp.At); d < fade {
					g = math.Min(g, d/fade)
				}
			}
			x := float64(v) * gain * g
			j := int((tl.VOStart + shift + tau) * sampleRate)
			vo.add(j, x, 0)
			a := math.Abs(x)
			if a > e {
				e += (a - e) * 0.01
			} else {
				e += (a - e) * 0.0002
			}
			if j >= 0 && j < len(env) {
				env[j] = float32(e)
			}
		}
	}

	master := newStereo(length)
	for i := range master.L {
		duck := 1 - 0.75*math.Min(1, float64(env[i])/0.06)
		for _, p := range []struct {
			bus   *stereo
			gain  float64
			chain bool
			duck  float64
		}{
			{b.drums, 0.5, false, 0.5},
			{b.bass, 0.55, true, 1},
			{b.synth, 0.55, true, 1},
			{b.fx, 1, false, 0.7},
		} {
			g := p.gain * (1 - (1-duck)*p.duck)
			if p.chain {
				g *= float64(sc[i])
			}
			p.bus.L[i] *= float32(g)
			p.bus.R[i] *= float32(g)
		}
	}
	master.mix(b.drums, 1)
	master.mix(reverb(b.drums, 0.5, 0.5, 1), 0.12)
	master.mix(b.bass, 1)
	master.mix(b.synth, 1)
	master.mix(reverb(b.synth, 0.85, 0.3, 1), 0.4)
	master.mix(b.fx, 1)
	master.mix(reverb(b.fx, 0.8, 0.35, 1), 0.3)
	if stems != "" {
		if err := writeStems(stems, master, vo); err != nil {
			return err
		}
	}
	master.mix(vo, 1)
	master.mix(reverb(vo, 0.3, 0.5, 1), 0.05)

	fadeFrom := int((tl.Duration - 0.8) * sampleRate)
	peak := 0.0
	for i := range master.L {
		g := 1.0
		if i > fadeFrom {
			g = math.Max(0, 1-float64(i-fadeFrom)/(0.8*sampleRate))
		}
		for _, ch := range [][]float32{master.L, master.R} {
			x := math.Tanh(float64(ch[i])*g*1.2) / 1.2
			ch[i] = float32(x)
			peak = math.Max(peak, math.Abs(x))
		}
	}
	norm := 1.0
	if peak > 0.89 {
		norm = 0.89 / peak
	}
	fmt.Printf("audio: peak %.2f, voice-over %v\n", peak, tl.Recorded)
	return writeWAV(path, master, norm)
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

// normalizeLoudness brings the mix to -14 LUFS with a two-pass, linear
// ffmpeg loudnorm, the usual level for online video.
func normalizeLoudness(in, out string) error {
	const target = "I=-14:TP=-1.0:LRA=11"
	measure := exec.Command("ffmpeg", "-hide_banner", "-nostats", "-i", in,
		"-af", "loudnorm="+target+":print_format=json", "-f", "null", "-")
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
	af := fmt.Sprintf("loudnorm=%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true",
		target, m["input_i"], m["input_tp"], m["input_lra"], m["input_thresh"], m["target_offset"])
	apply := exec.Command("ffmpeg", "-y", "-v", "error", "-i", in, "-af", af, "-ar", fmt.Sprint(sampleRate), "-c:a", "pcm_s16le", out)
	apply.Stderr = os.Stderr
	if err := apply.Run(); err != nil {
		return fmt.Errorf("normalize loudness: %w", err)
	}
	fmt.Printf("audio: loudness %s LUFS -> -14 LUFS\n", m["input_i"])
	return nil
}

// writeStems saves the music-and-effects bed and the voice separately, at
// the same gain and before limiting, so their balance can be measured.
func writeStems(dir string, bed, vo *stereo) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeWAV(filepath.Join(dir, "bed.wav"), bed, 1); err != nil {
		return err
	}
	return writeWAV(filepath.Join(dir, "voice.wav"), vo, 1)
}
