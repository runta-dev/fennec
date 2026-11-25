package main

import (
	"math"
	"math/rand"
)

const sampleRate = 48000

// stereo is a float buffer at sampleRate.
type stereo struct{ L, R []float32 }

func newStereo(seconds float64) *stereo {
	n := int(seconds * sampleRate)
	return &stereo{L: make([]float32, n), R: make([]float32, n)}
}

func (s *stereo) len() int { return len(s.L) }

// add writes a sample with equal-power panning (-1 left .. 1 right).
func (s *stereo) add(i int, v, pan float64) {
	if i < 0 || i >= len(s.L) {
		return
	}
	a := (pan + 1) * math.Pi / 4
	s.L[i] += float32(v * math.Cos(a))
	s.R[i] += float32(v * math.Sin(a))
}

func (s *stereo) mix(o *stereo, gain float64) {
	for i := range s.L {
		if i >= len(o.L) {
			break
		}
		s.L[i] += o.L[i] * float32(gain)
		s.R[i] += o.R[i] * float32(gain)
	}
}

func midiHz(n float64) float64 { return 440 * math.Pow(2, (n-69)/12) }

// piano renders a soft, felt-piano-like note: inharmonic partials whose
// upper harmonics decay faster, a gentle hammer thump and a release tail.
func piano(dst *stereo, at, hold, note, vel, pan float64) {
	f := midiHz(note)
	start := int(at * sampleRate)
	release := 0.6
	total := hold + release
	if total > 7 {
		total = 7
	}
	n := int(total * sampleRate)
	baseTau := 2.6 * math.Pow(220/f, 0.45)
	if baseTau > 4.5 {
		baseTau = 4.5
	}
	type partial struct{ f, a, tau float64 }
	var ps []partial
	for k := 1; k <= 9; k++ {
		fk := float64(k) * f * math.Sqrt(1+0.00035*float64(k*k))
		if fk > 12000 {
			break
		}
		a := 1 / math.Pow(float64(k), 1.55)
		// Felt dampens the upper partials; brighter on harder hits.
		a *= math.Exp(-float64(k-1) * (0.34 - 0.16*vel))
		ps = append(ps, partial{fk, a, baseTau / (1 + 0.55*float64(k-1))})
	}
	detune := 1.0007
	rng := rand.New(rand.NewSource(int64(note*1000 + at*100)))
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := 1.0
		if t < 0.004 {
			env = t / 0.004
		}
		if t > hold {
			env *= math.Exp(-(t - hold) / 0.18)
		}
		v := 0.0
		for _, p := range ps {
			d := math.Exp(-t/p.tau) * (0.75 + 0.25*math.Exp(-t/0.12))
			v += p.a * d * (math.Sin(2*math.Pi*p.f*t) + 0.5*math.Sin(2*math.Pi*p.f*detune*t))
		}
		v *= 0.66
		if t < 0.012 {
			v += (rng.Float64()*2 - 1) * 0.04 * vel * (1 - t/0.012)
		}
		dst.add(start+i, v*env*vel*0.22, pan)
	}
}

// pad renders a slow, warm chord voice from detuned band-limited saws.
func pad(dst *stereo, at, dur, note, gain, pan float64) {
	f := midiHz(note)
	start := int(at * sampleRate)
	attack, rel := 1.6, 2.2
	n := int((dur + rel) * sampleRate)
	detunes := []float64{-0.09, 0, 0.08}
	var harm []float64
	for k := 1; k <= 14; k++ {
		if float64(k)*f > 5000 {
			break
		}
		harm = append(harm, math.Exp(-float64(k)*f/1400)/float64(k))
	}
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := math.Min(1, t/attack)
		env = env * env * (3 - 2*env)
		if t > dur {
			env *= math.Exp(-(t - dur) / (rel / 3))
		}
		lfo := 1 + 0.0015*math.Sin(2*math.Pi*0.18*t)
		v := 0.0
		for di, d := range detunes {
			fd := f * math.Pow(2, d/12) * lfo
			ph := float64(di) * 1.7
			for k, a := range harm {
				v += a * math.Sin(2*math.Pi*fd*float64(k+1)*t+ph*float64(k+1))
			}
		}
		dst.add(start+i, v*env*gain*0.05, pan+0.25*math.Sin(2*math.Pi*0.07*t+note))
	}
}

// bass is a round sine bass with a little second harmonic.
func bass(dst *stereo, at, dur, note, gain float64) {
	f := midiHz(note)
	start := int(at * sampleRate)
	n := int((dur + 0.5) * sampleRate)
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := math.Min(1, t/0.03) * math.Exp(-t/3.5)
		if t > dur {
			env *= math.Exp(-(t - dur) / 0.12)
		}
		v := math.Sin(2*math.Pi*f*t) + 0.18*math.Sin(4*math.Pi*f*t)
		dst.add(start+i, v*env*gain*0.16, 0)
	}
}

// bell renders an FM bell, used for the logo chimes.
func bell(dst *stereo, at, note, gain, pan, decay float64) {
	f := midiHz(note)
	start := int(at * sampleRate)
	n := int(decay * 1.6 * sampleRate)
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := math.Min(1, t/0.002) * math.Exp(-t/(decay/3))
		idx := 2.2 * math.Exp(-t/0.25)
		v := math.Sin(2*math.Pi*f*t + idx*math.Sin(2*math.Pi*f*3.5*t))
		v += 0.3 * math.Sin(2*math.Pi*f*2.01*t) * math.Exp(-t/0.6)
		dst.add(start+i, v*env*gain, pan)
	}
}

// svf is a Chamberlin state-variable filter.
type svf struct{ low, band float64 }

func (s *svf) step(in, fc, q float64) (low, band, high float64) {
	f := 2 * math.Sin(math.Pi*math.Min(fc, sampleRate/6)/sampleRate)
	s.low += f * s.band
	high = in - s.low - q*s.band
	s.band += f * high
	return s.low, s.band, high
}

// noiseSweep is a band-passed noise swell (whoosh).
func noiseSweep(dst *stereo, at, dur, f0, f1, gain float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	start := int(at * sampleRate)
	n := int(dur * sampleRate)
	var fl, fr svf
	for i := 0; i < n; i++ {
		p := float64(i) / float64(n)
		fc := f0 * math.Pow(f1/f0, p)
		env := math.Sin(math.Pi * math.Pow(p, 0.7))
		env *= env
		_, bl, _ := fl.step(rng.Float64()*2-1, fc, 0.9)
		_, br, _ := fr.step(rng.Float64()*2-1, fc*1.03, 0.9)
		pan := -0.6 + 1.2*p
		a := (pan + 1) * math.Pi / 4
		if j := start + i; j >= 0 && j < dst.len() {
			dst.L[j] += float32(bl * env * gain * math.Cos(a))
			dst.R[j] += float32(br * env * gain * math.Sin(a))
		}
	}
}

// keyClick is a single keyboard keystroke.
func keyClick(dst *stereo, at, gain, pan float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	start := int(at * sampleRate)
	n := int(0.05 * sampleRate)
	var f svf
	fc := 2600 + rng.Float64()*1400
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := math.Exp(-t / 0.006)
		_, b, _ := f.step(rng.Float64()*2-1, fc, 0.5)
		thock := math.Sin(2*math.Pi*180*t) * math.Exp(-t/0.012) * 0.6
		dst.add(start+i, (b*0.8+thock)*env*gain, pan)
	}
}

// lidThump is a laptop lid closing: a soft low thud and a magnet click.
func lidThump(dst *stereo, at, gain float64, open bool) {
	start := int(at * sampleRate)
	n := int(0.45 * sampleRate)
	rng := rand.New(rand.NewSource(int64(at * 1000)))
	var f svf
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		fr := 62 + 40*math.Exp(-t/0.04)
		v := math.Sin(2*math.Pi*fr*t) * math.Exp(-t/0.09)
		_, b, _ := f.step(rng.Float64()*2-1, 1800, 0.7)
		v += b * math.Exp(-t/0.004) * 0.7
		if open {
			// Opening is a quieter swish without the thud.
			v = b * math.Sin(math.Pi*math.Min(1, t/0.4)) * 0.25
		}
		dst.add(start+i, v*gain*math.Min(1, t/0.0015), 0)
	}
}

// blip is a short UI pop.
func blip(dst *stereo, at, f, gain, pan float64) {
	start := int(at * sampleRate)
	n := int(0.12 * sampleRate)
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		fr := f * (1 + 0.5*math.Exp(-t/0.01))
		v := math.Sin(2*math.Pi*fr*t) * math.Exp(-t/0.03) * math.Min(1, t/0.002)
		dst.add(start+i, v*gain, pan)
	}
}

// reverb is a small Freeverb.
func reverb(src *stereo, room, damp, wet float64) *stereo {
	combL := []int{1557, 1617, 1491, 1422, 1277, 1356, 1188, 1116}
	apL := []int{556, 441, 341, 225}
	scale := float64(sampleRate) / 44100
	out := &stereo{L: make([]float32, src.len()), R: make([]float32, src.len())}
	for ch := 0; ch < 2; ch++ {
		in := src.L
		dst := out.L
		spread := 0
		if ch == 1 {
			in, dst, spread = src.R, out.R, 23
		}
		type comb struct {
			buf   []float64
			i     int
			store float64
		}
		combs := make([]comb, len(combL))
		for k, l := range combL {
			combs[k].buf = make([]float64, int(float64(l+spread)*scale))
		}
		aps := make([]comb, len(apL))
		for k, l := range apL {
			aps[k].buf = make([]float64, int(float64(l+spread)*scale))
		}
		fb := 0.7 + 0.28*room
		// High-pass the send so the tail stays clear of the bass.
		hpA := 1 / (1 + 2*math.Pi*160/sampleRate)
		var hpY, hpX float64
		for i := range in {
			hpY = hpA * (hpY + float64(in[i]) - hpX)
			hpX = float64(in[i])
			x := hpY * 0.015
			acc := 0.0
			for k := range combs {
				c := &combs[k]
				y := c.buf[c.i]
				c.store = y*(1-damp) + c.store*damp
				c.buf[c.i] = x + c.store*fb
				c.i = (c.i + 1) % len(c.buf)
				acc += y
			}
			for k := range aps {
				a := &aps[k]
				b := a.buf[a.i]
				a.buf[a.i] = acc + b*0.5
				acc = b - acc
				a.i = (a.i + 1) % len(a.buf)
			}
			dst[i] = float32(acc * wet * 3)
		}
	}
	return out
}
