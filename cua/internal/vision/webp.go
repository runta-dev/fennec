package vision

import (
	"container/heap"
	"encoding/binary"
	"errors"
	"sort"
)

// This file writes lossless WebP (VP8L): the subtract-green transform, one
// group of prefix codes, and LZ77 copies from the left and upper neighbours,
// which is what flat UI screenshots need. No colour cache, no predictor.

const (
	vp8lMaxSide   = 1 << 14
	vp8lMaxLength = 4096
	vp8lMinCopy   = 3
	// Distance codes of the two neighbours in the 2D distance map.
	distUp   = 1
	distLeft = 2
	// Alphabet sizes: green with 24 length prefixes, red, blue, alpha, distance.
	greenAlphabet    = 256 + 24
	distanceAlphabet = 40
	maxCodeLength    = 15
	maxCodeLenCode   = 7
)

// codeLengthOrder is the order the code-length code's lengths are written in.
var codeLengthOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// EncodeWebP writes the frame as lossless WebP.
func EncodeWebP(f *Frame) ([]byte, error) {
	if f.W < 1 || f.H < 1 || f.W > vp8lMaxSide || f.H > vp8lMaxSide {
		return nil, errors.New("encode WebP: the image is empty or larger than 16384 pixels on a side")
	}
	n := f.W * f.H
	// Pixels after subtract-green, packed as 0xRRGGBB (alpha is always 255).
	px := make([]uint32, n)
	for i := range px {
		r, g, b := f.Pix[i*3], f.Pix[i*3+1], f.Pix[i*3+2]
		px[i] = uint32(r-g)<<16 | uint32(g)<<8 | uint32(b-g)
	}
	toks := tokenize(px, f.W)
	var green [greenAlphabet]int
	var red, blue, alpha [256]int
	var dist [distanceAlphabet]int
	for _, t := range toks {
		if t.length == 0 {
			green[t.pixel>>8&0xff]++
			red[t.pixel>>16&0xff]++
			blue[t.pixel&0xff]++
			alpha[0xff]++
			continue
		}
		p, _, _ := prefixOf(t.length)
		green[256+p]++
		d, _, _ := prefixOf(t.dist)
		dist[d]++
	}
	var w bitWriter
	w.bits(0x2f, 8)
	w.bits(uint32(f.W-1), 14)
	w.bits(uint32(f.H-1), 14)
	w.bits(0, 1) // no alpha in use
	w.bits(0, 3) // version 0
	w.bits(1, 1) // a transform:
	w.bits(2, 2) // subtract green
	w.bits(0, 1) // no more transforms
	w.bits(0, 1) // no colour cache
	w.bits(0, 1) // one prefix code group
	codes := [5]*prefixCode{
		writeCode(&w, green[:]),
		writeCode(&w, red[:]),
		writeCode(&w, blue[:]),
		writeCode(&w, alpha[:]),
		writeCode(&w, dist[:]),
	}
	for _, t := range toks {
		if t.length == 0 {
			codes[0].put(&w, int(t.pixel>>8&0xff))
			codes[1].put(&w, int(t.pixel>>16&0xff))
			codes[2].put(&w, int(t.pixel&0xff))
			codes[3].put(&w, 0xff)
			continue
		}
		p, nb, extra := prefixOf(t.length)
		codes[0].put(&w, 256+p)
		w.bits(extra, nb)
		p, nb, extra = prefixOf(t.dist)
		codes[4].put(&w, p)
		w.bits(extra, nb)
	}
	data := w.flush()
	chunk := len(data)
	pad := chunk & 1
	out := make([]byte, 0, 20+chunk+pad)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+chunk+pad))
	out = append(out, "WEBPVP8L"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(chunk))
	out = append(out, data...)
	if pad == 1 {
		out = append(out, 0)
	}
	return out, nil
}

type token struct {
	pixel  uint32
	length int // 0 for a literal
	dist   int // distance code
}

// tokenize picks, at each pixel, the longer copy from the left or the upper
// neighbour, or a literal when neither runs for vp8lMinCopy pixels.
func tokenize(px []uint32, width int) []token {
	var out []token
	n := len(px)
	for i := 0; i < n; {
		best, code := 0, 0
		for _, c := range [2]struct{ d, code int }{{1, distLeft}, {width, distUp}} {
			if i < c.d {
				continue
			}
			l := 0
			for l < vp8lMaxLength && i+l < n && px[i+l] == px[i+l-c.d] {
				l++
			}
			if l > best {
				best, code = l, c.code
			}
		}
		if best >= vp8lMinCopy {
			out = append(out, token{length: best, dist: code})
			i += best
			continue
		}
		out = append(out, token{pixel: px[i]})
		i++
	}
	return out
}

// prefixOf splits an LZ77 value (1-based) into its prefix symbol and extra bits.
func prefixOf(value int) (symbol int, nbits int, extra uint32) {
	v := value - 1
	if v < 4 {
		return v, 0, 0
	}
	hi := 31
	for v>>hi == 0 {
		hi--
	}
	second := (v >> (hi - 1)) & 1
	nbits = hi - 1
	return 2*hi + second, nbits, uint32(v & (1<<nbits - 1))
}

// prefixCode is a canonical prefix code ready to write.
type prefixCode struct {
	codes []uint32 // bit-reversed for LSB-first output
	lens  []uint8  // bits written per symbol (0 for a one-symbol code)
}

func (c *prefixCode) put(w *bitWriter, sym int) {
	if n := c.lens[sym]; n > 0 {
		w.bits(c.codes[sym], int(n))
	}
}

// writeCode writes the prefix code for freq and returns it.
func writeCode(w *bitWriter, freq []int) *prefixCode {
	var used []int
	for s, f := range freq {
		if f > 0 {
			used = append(used, s)
		}
	}
	if len(used) == 0 {
		used = []int{0}
	}
	if len(used) <= 2 && used[len(used)-1] < 256 {
		// A simple code: one symbol costs no bits, two cost one each.
		c := &prefixCode{codes: make([]uint32, len(freq)), lens: make([]uint8, len(freq))}
		w.bits(1, 1)
		w.bits(uint32(len(used)-1), 1)
		if used[0] < 2 {
			w.bits(0, 1)
			w.bits(uint32(used[0]), 1)
		} else {
			w.bits(1, 1)
			w.bits(uint32(used[0]), 8)
		}
		if len(used) == 2 {
			w.bits(uint32(used[1]), 8)
			c.codes[used[1]], c.lens[used[0]], c.lens[used[1]] = 1, 1, 1
		}
		return c
	}
	lengths := huffmanLengths(freq, maxCodeLength)
	writeLengths(w, lengths)
	return canonical(lengths)
}

// writeLengths writes a normal code's lengths through the code-length code.
func writeLengths(w *bitWriter, lengths []uint8) {
	type rl struct {
		sym   int
		extra uint32
		nbits int
	}
	var seq []rl
	prev := uint8(8)
	for i := 0; i < len(lengths); {
		l := lengths[i]
		run := 1
		for i+run < len(lengths) && lengths[i+run] == l {
			run++
		}
		i += run
		if l == 0 {
			for run > 0 {
				switch {
				case run >= 11:
					k := min(run, 138)
					seq = append(seq, rl{18, uint32(k - 11), 7})
					run -= k
				case run >= 3:
					seq = append(seq, rl{17, uint32(run - 3), 3})
					run = 0
				default:
					seq = append(seq, rl{0, 0, 0})
					run--
				}
			}
			continue
		}
		if l != prev {
			seq = append(seq, rl{int(l), 0, 0})
			run--
			prev = l
		}
		for run > 0 {
			if run >= 3 {
				k := min(run, 6)
				seq = append(seq, rl{16, uint32(k - 3), 2})
				run -= k
				continue
			}
			seq = append(seq, rl{int(l), 0, 0})
			run--
		}
	}
	var freq [19]int
	for _, s := range seq {
		freq[s.sym]++
	}
	clLengths := huffmanLengths(freq[:], maxCodeLenCode)
	count := 4
	for i, s := range codeLengthOrder {
		if clLengths[s] > 0 {
			count = max(count, i+1)
		}
	}
	w.bits(0, 1) // a normal code
	w.bits(uint32(count-4), 4)
	for _, s := range codeLengthOrder[:count] {
		w.bits(uint32(clLengths[s]), 3)
	}
	w.bits(0, 1) // lengths for the whole alphabet
	cl := canonical(clLengths)
	for _, s := range seq {
		cl.put(w, s.sym)
		if s.nbits > 0 {
			w.bits(s.extra, s.nbits)
		}
	}
}

// canonical assigns canonical codes to lengths, as the decoder rebuilds
// them. A code with a single used symbol reads no bits.
func canonical(lengths []uint8) *prefixCode {
	c := &prefixCode{codes: make([]uint32, len(lengths)), lens: make([]uint8, len(lengths))}
	used := 0
	var count [maxCodeLength + 2]uint32
	for _, l := range lengths {
		if l > 0 {
			used++
			count[l]++
		}
	}
	if used == 1 {
		return c
	}
	var next [maxCodeLength + 2]uint32
	code := uint32(0)
	for bits := 1; bits <= maxCodeLength; bits++ {
		code = (code + count[bits-1]) << 1
		next[bits] = code
	}
	for s, l := range lengths {
		if l == 0 {
			continue
		}
		v := next[l]
		next[l]++
		var r uint32
		for i := uint8(0); i < l; i++ {
			r = r<<1 | (v>>i)&1
		}
		c.codes[s], c.lens[s] = r, l
	}
	return c
}

// huffmanLengths builds Huffman code lengths no longer than limit, halving
// the frequencies until the tree is shallow enough.
func huffmanLengths(freq []int, limit int) []uint8 {
	f := append([]int(nil), freq...)
	for {
		lengths, deepest := huffman(f)
		if deepest <= limit {
			return lengths
		}
		for i, v := range f {
			if v > 0 {
				f[i] = (v + 1) / 2
			}
		}
	}
}

type hnode struct {
	weight int
	sym    int // -1 for an inner node
	kids   [2]*hnode
	order  int
}

type hheap []*hnode

func (h hheap) Len() int { return len(h) }
func (h hheap) Less(i, j int) bool {
	if h[i].weight != h[j].weight {
		return h[i].weight < h[j].weight
	}
	return h[i].order < h[j].order
}
func (h hheap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *hheap) Push(x any)   { *h = append(*h, x.(*hnode)) }
func (h *hheap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

func huffman(freq []int) ([]uint8, int) {
	lengths := make([]uint8, len(freq))
	var h hheap
	for s, f := range freq {
		if f > 0 {
			h = append(h, &hnode{weight: f, sym: s, order: s})
		}
	}
	switch len(h) {
	case 0:
		return lengths, 0
	case 1:
		lengths[h[0].sym] = 1
		return lengths, 1
	}
	sort.Sort(h)
	heap.Init(&h)
	order := len(freq)
	for h.Len() > 1 {
		a := heap.Pop(&h).(*hnode)
		b := heap.Pop(&h).(*hnode)
		heap.Push(&h, &hnode{weight: a.weight + b.weight, sym: -1, kids: [2]*hnode{a, b}, order: order})
		order++
	}
	deepest := 0
	var walk func(n *hnode, depth int)
	walk = func(n *hnode, depth int) {
		if n.sym >= 0 {
			lengths[n.sym] = uint8(min(depth, 255))
			deepest = max(deepest, depth)
			return
		}
		walk(n.kids[0], depth+1)
		walk(n.kids[1], depth+1)
	}
	walk(h[0], 0)
	return lengths, deepest
}

// bitWriter packs bits least significant first.
type bitWriter struct {
	out  []byte
	acc  uint64
	used int
}

func (w *bitWriter) bits(v uint32, n int) {
	w.acc |= uint64(v&(1<<n-1)) << w.used
	w.used += n
	for w.used >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.used -= 8
	}
}

func (w *bitWriter) flush() []byte {
	if w.used > 0 {
		w.out = append(w.out, byte(w.acc))
		w.acc, w.used = 0, 0
	}
	return w.out
}
