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

const targetLUFS = -16.0

// duckEnvelope turns the voice into a music gain (1 = no ducking, 0.25 =
// fully ducked). It follows a 25 ms RMS of the voice and smooths the gain
// itself (40 ms down, 300 ms up). A faster follower tracks the voice
// waveform and modulates the music at the voice's pitch, which sounds like
// an electrical buzz.
func duckEnvelope(v []float32) []float32 {
	out := make([]float32, len(v))
	kRMS := 1 / (0.025 * sampleRate)
	kDown := 1 / (0.04 * sampleRate)
	kUp := 1 / (0.3 * sampleRate)
	ms, g := 0.0, 1.0
	for i, x := range v {
		ms += (float64(x)*float64(x) - ms) * kRMS
		want := 1 - 0.75*math.Min(1, math.Sqrt(ms)/0.05)
		if want < g {
			g += (want - g) * kDown
		} else {
			g += (want - g) * kUp
		}
		out[i] = float32(g)
	}
	return out
}

// limit is a look-ahead peak limiter: the gain starts falling before a peak
// arrives and recovers slowly, so loud moments are turned down instead of
// clipped. It returns the largest reduction in dB and the share of samples
// reduced by more than 1 dB.
func limit(s *stereo, ceiling, lookahead, release float64) (maxGR, share float64) {
	n := s.len()
	la := int(lookahead * sampleRate)
	need := make([]float64, n)
	for i := 0; i < n; i++ {
		p := math.Max(math.Abs(float64(s.L[i])), math.Abs(float64(s.R[i])))
		need[i] = 1
		if p > ceiling {
			need[i] = ceiling / p
		}
	}
	// Minimum over the next `la` samples (monotonic deque).
	gmin := make([]float64, n)
	dq := make([]int, 0, la+1)
	for i := n - 1; i >= 0; i-- {
		for len(dq) > 0 && need[dq[len(dq)-1]] >= need[i] {
			dq = dq[:len(dq)-1]
		}
		dq = append(dq, i)
		if dq[0] > i+la {
			dq = dq[1:]
		}
		gmin[i] = need[dq[0]]
	}
	// Ramp down ahead of peaks (backward), then release (forward).
	ka := 1 - math.Exp(-3/float64(max(1, la)))
	for i := n - 2; i >= 0; i-- {
		gmin[i] = math.Min(gmin[i], gmin[i+1]+(1-gmin[i+1])*ka)
	}
	kr := 1 - math.Exp(-1/(release*sampleRate))
	g := 1.0
	reduced := 0
	for i := 0; i < n; i++ {
		g = math.Min(gmin[i], g+(1-g)*kr)
		s.L[i] *= float32(g)
		s.R[i] *= float32(g)
		gr := -20 * math.Log10(g)
		maxGR = math.Max(maxGR, gr)
		if gr > 1 {
			reduced++
		}
	}
	return maxGR, float64(reduced) / float64(max(1, n))
}

func peakOf(s *stereo) float64 {
	p := 0.0
	for i := range s.L {
		p = math.Max(p, math.Max(math.Abs(float64(s.L[i])), math.Abs(float64(s.R[i]))))
	}
	return p
}

// master2file brings the mix to targetLUFS with one gain, catches the peaks
// with the limiter at -2 dBFS and writes a 16-bit WAV. Nothing is clipped.
func master2file(m *stereo, path string) error {
	peak := peakOf(m)
	if peak == 0 {
		return writeWAV(path, m, 1)
	}
	probe := path + ".probe.wav"
	if err := writeWAV(probe, m, 0.5/peak); err != nil {
		return err
	}
	lufs, err := measureLUFS(probe)
	os.Remove(probe)
	if err != nil {
		return err
	}
	gain := 0.5 / peak * math.Pow(10, (targetLUFS-lufs)/20)
	for i := range m.L {
		m.L[i] *= float32(gain)
		m.R[i] *= float32(gain)
	}
	maxGR, share := limit(m, 0.8, 0.005, 0.12)
	fmt.Printf("audio: %.1f LUFS, limiter max %.1f dB, %.1f%% of samples reduced >1 dB\n", targetLUFS, maxGR, share*100)
	return writeWAV(path, m, 1)
}

// measureLUFS returns the integrated loudness of a file.
func measureLUFS(path string) (float64, error) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-nostats", "-i", path,
		"-af", "loudnorm=print_format=json", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("measure loudness: %w", err)
	}
	s := stderr.String()
	i, j := strings.LastIndex(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return 0, fmt.Errorf("measure loudness: no result")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s[i:j+1]), &m); err != nil {
		return 0, err
	}
	var v float64
	if _, err := fmt.Sscan(m["input_i"], &v); err != nil {
		return 0, fmt.Errorf("measure loudness: %q", m["input_i"])
	}
	return v, nil
}

// writeStems saves the music-and-effects bed and the voice with one shared
// gain (so their balance is kept) and enough headroom not to clip.
func writeStems(dir string, bed, vo *stereo) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	g := 0.5 / math.Max(1e-9, math.Max(peakOf(bed), peakOf(vo)))
	if err := writeWAV(filepath.Join(dir, "bed.wav"), bed, g); err != nil {
		return err
	}
	return writeWAV(filepath.Join(dir, "voice.wav"), vo, g)
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
		pcm[2*i] = int16(math.Round(math.Max(-1, math.Min(1, float64(s.L[i])*gain)) * 32767))
		pcm[2*i+1] = int16(math.Round(math.Max(-1, math.Min(1, float64(s.R[i])*gain)) * 32767))
	}
	w(pcm)
	return os.WriteFile(path, b.Bytes(), 0o644)
}
