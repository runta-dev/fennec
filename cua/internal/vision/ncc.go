package vision

import "math"

// MatchBar is the lowest normalized cross-correlation that accepts a
// recorded crop's match.
const MatchBar = 0.8

// flatSpread is the luma variance below which a window is too flat to
// match anything meaningfully.
const flatSpread = 4

// Match is where a template best matches an image.
type Match struct {
	// X, Y is the match's centre in image pixels.
	X, Y  float64
	Score float64 // normalized cross-correlation, -1 when nothing matched
}

// plane is a luma raster.
type plane struct {
	w, h int
	v    []float64
}

// lumaPlane converts a frame to luma, averaging f by f blocks (f >= 1) so
// images of different pixels per desktop unit compare at one scale.
func lumaPlane(fr *Frame, f float64) plane {
	f = math.Max(f, 1e-9)
	ow, oh := max(1, int(float64(fr.W)/f)), max(1, int(float64(fr.H)/f))
	p := plane{w: ow, h: oh, v: make([]float64, ow*oh)}
	for oy := 0; oy < oh; oy++ {
		y0 := min(fr.H-1, int(float64(oy)*f))
		y1 := max(y0+1, min(fr.H, int(float64(oy+1)*f)))
		for ox := 0; ox < ow; ox++ {
			x0 := min(fr.W-1, int(float64(ox)*f))
			x1 := max(x0+1, min(fr.W, int(float64(ox+1)*f)))
			var sum float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					i := (y*fr.W + x) * 3
					sum += 0.299*float64(fr.Pix[i]) + 0.587*float64(fr.Pix[i+1]) + 0.114*float64(fr.Pix[i+2])
				}
			}
			p.v[oy*ow+ox] = sum / float64((y1-y0)*(x1-x0))
		}
	}
	return p
}

// FindTemplate searches img for tmpl by normalized cross-correlation.
// imgPer and tmplPer are each image's pixels per desktop unit, so a crop
// recorded at another backing scale still matches; both are compared at
// one pixel per desktop unit. The search runs coarse first, then refines
// around the best coarse spot. The match centre is in img pixels.
func FindTemplate(img, tmpl *Frame, imgPer, tmplPer float64) Match {
	if !Positive(imgPer) {
		imgPer = 1
	}
	if !Positive(tmplPer) {
		tmplPer = 1
	}
	coarse := 1.0
	if c := math.Floor(math.Min(float64(tmpl.W)/tmplPer, float64(tmpl.H)/tmplPer) / 8); c > 1 {
		coarse = math.Min(4, c)
	}
	hay, pin := lumaPlane(img, imgPer*coarse), lumaPlane(tmpl, tmplPer*coarse)
	_, cx, cy := bestMatch(hay, pin, 0, 0, hay.w-pin.w, hay.h-pin.h)
	fine, pin1 := lumaPlane(img, imgPer), lumaPlane(tmpl, tmplPer)
	c := int(coarse)
	score, x, y := bestMatch(fine, pin1, cx*c-c, cy*c-c, cx*c+c, cy*c+c)
	return Match{X: (float64(x) + float64(pin1.w)/2) * imgPer, Y: (float64(y) + float64(pin1.h)/2) * imgPer, Score: score}
}

// bestMatch scores every placement of pin's top-left corner within the
// given range; a flat window scores nothing.
func bestMatch(hay, pin plane, x0, y0, x1, y1 int) (float64, int, int) {
	n := float64(pin.w * pin.h)
	var mean float64
	for _, a := range pin.v {
		mean += a
	}
	mean /= n
	t := make([]float64, len(pin.v))
	var sq float64
	for i, a := range pin.v {
		t[i] = a - mean
		sq += t[i] * t[i]
	}
	sd := math.Sqrt(sq / n)
	if sd == 0 {
		sd = 1
	}
	best, bx, by := -1.0, max(0, x0), max(0, y0)
	for y := max(0, y0); y <= min(hay.h-pin.h, y1); y++ {
		for x := max(0, x0); x <= min(hay.w-pin.w, x1); x++ {
			var s, s2, dot float64
			for j := 0; j < pin.h; j++ {
				row := (y+j)*hay.w + x
				trow := j * pin.w
				for i := 0; i < pin.w; i++ {
					a := hay.v[row+i]
					s += a
					s2 += a * a
					dot += a * t[trow+i]
				}
			}
			spread := s2/n - (s/n)*(s/n)
			if spread < flatSpread {
				continue
			}
			if score := dot / n / (math.Sqrt(spread) * sd); score > best {
				best, bx, by = score, x, y
			}
		}
	}
	return best, bx, by
}
