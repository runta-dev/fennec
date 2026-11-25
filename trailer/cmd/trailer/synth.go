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

// kick is a punchy electronic kick: a fast pitch drop and a short click.
func kick(dst *stereo, at, gain float64) {
	start := int(at * sampleRate)
	n := int(0.5 * sampleRate)
	ph := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		f := 46 + 120*math.Exp(-t/0.03)
		ph += 2 * math.Pi * f / sampleRate
		v := math.Sin(ph) * math.Exp(-t/0.24)
		v += math.Sin(2*math.Pi*3200*t) * math.Exp(-t/0.0015) * 0.25
		dst.add(start+i, math.Tanh(v*1.4)*gain*0.5, 0)
	}
}

// clap is three quick noise bursts and a short tail, band-passed.
func clap(dst *stereo, at, gain float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	start := int(at * sampleRate)
	n := int(0.3 * sampleRate)
	var fl, fr svf
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := 0.0
		for _, b := range []float64{0, 0.011, 0.023} {
			if t >= b {
				env = math.Max(env, math.Exp(-(t-b)/0.005))
			}
		}
		env = math.Max(env, 0.6*math.Exp(-(t-0.023)/0.07)*b2f(t >= 0.023))
		_, bl, _ := fl.step(rng.Float64()*2-1, 1300, 0.55)
		_, br, _ := fr.step(rng.Float64()*2-1, 1450, 0.55)
		if j := start + i; j >= 0 && j < dst.len() {
			dst.L[j] += float32(bl * env * gain * 0.7)
			dst.R[j] += float32(br * env * gain * 0.7)
		}
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// hat is a high-passed noise tick; open hats ring longer.
func hat(dst *stereo, at, gain, decay, pan float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	start := int(at * sampleRate)
	n := int(decay * 6 * sampleRate)
	var f svf
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		_, _, hp := f.step(rng.Float64()*2-1, 7000, 0.7)
		dst.add(start+i, hp*math.Exp(-t/decay)*gain*0.5, pan)
	}
}

// pluck is a bright saw pluck through a decaying low-pass filter.
func pluck(dst *stereo, at, note, gain, pan, cutoff, decay float64) {
	f := midiHz(note)
	start := int(at * sampleRate)
	n := int(decay * 5 * sampleRate)
	var flt svf
	nh := int(math.Min(24, 9000/f))
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		v := 0.0
		for k := 1; k <= nh; k++ {
			v += math.Sin(2*math.Pi*f*float64(k)*t) / float64(k)
		}
		v += 0.5 * math.Sin(2*math.Pi*f*1.006*t)
		fc := cutoff * (1 + 4*math.Exp(-t/0.04))
		lp, _, _ := flt.step(v, fc, 0.6)
		env := math.Min(1, t/0.002) * math.Exp(-t/decay)
		dst.add(start+i, lp*env*gain*0.12, pan)
	}
}

// crash is a long high-passed noise wash.
func crash(dst *stereo, at, gain float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	start := int(at * sampleRate)
	n := int(2.2 * sampleRate)
	var fl, fr svf
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := math.Exp(-t / 0.55)
		_, _, l := fl.step(rng.Float64()*2-1, 5200, 0.8)
		_, _, r := fr.step(rng.Float64()*2-1, 5400, 0.8)
		if j := start + i; j >= 0 && j < dst.len() {
			dst.L[j] += float32(l * env * gain * 0.35)
			dst.R[j] += float32(r * env * gain * 0.35)
		}
	}
}

// impact is the drop hit: kick, sub boom and crash.
func impact(dst *stereo, at, gain float64, seed int64) {
	kick(dst, at, gain*1.2)
	start := int(at * sampleRate)
	n := int(1.6 * sampleRate)
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		f := 38 + 30*math.Exp(-t/0.12)
		v := math.Sin(2*math.Pi*f*t) * math.Exp(-t/0.6)
		dst.add(start+i, v*gain*0.45, 0)
	}
	crash(dst, at, gain, seed)
}

// powerDown is a falling tone, like a machine switching off.
func powerDown(dst *stereo, at, gain float64) {
	start := int(at * sampleRate)
	n := int(0.6 * sampleRate)
	ph := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		f := 30 + 260*math.Exp(-t/0.16)
		ph += 2 * math.Pi * f / sampleRate
		v := math.Sin(ph) + 0.35*math.Sin(2*ph) + 0.2*math.Sin(3*ph)
		env := math.Min(1, t/0.005) * (1 - t/0.6)
		dst.add(start+i, v*env*gain*0.3, 0)
	}
}

// riser is noise whose filter and level climb up to `end`.
func riser(dst *stereo, end, dur, gain float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	start := int((end - dur) * sampleRate)
	n := int(dur * sampleRate)
	var fl, fr svf
	for i := 0; i < n; i++ {
		p := float64(i) / float64(n)
		fc := 300 * math.Pow(25, p)
		_, bl, _ := fl.step(rng.Float64()*2-1, fc, 0.5)
		_, br, _ := fr.step(rng.Float64()*2-1, fc*1.05, 0.5)
		env := p * p * gain * 0.4
		if j := start + i; j >= 0 && j < dst.len() {
			dst.L[j] += float32(bl * env)
			dst.R[j] += float32(br * env)
		}
	}
}
