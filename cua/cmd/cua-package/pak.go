package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// pak is a Chromium resource pack (ui/base/resource/data_pack.cc,
// version 5): a header, a table of (id, offset) entries ending with a
// sentinel, a table of aliases (id, entry index), then the data.
type pak struct {
	encoding uint8
	ids      []uint16
	data     [][]byte
	aliases  [][2]uint16 // id, entry index
}

func parsePak(b []byte) (*pak, error) {
	if len(b) < 12 || binary.LittleEndian.Uint32(b) != 5 {
		return nil, errors.New("not a version 5 resource pack")
	}
	p := &pak{encoding: b[4]}
	n := int(binary.LittleEndian.Uint16(b[8:]))
	na := int(binary.LittleEndian.Uint16(b[10:]))
	off := 12
	if len(b) < off+(n+1)*6+na*4 {
		return nil, errors.New("truncated resource pack")
	}
	type ent struct {
		id  uint16
		off uint32
	}
	ents := make([]ent, n+1)
	for i := range ents {
		ents[i] = ent{binary.LittleEndian.Uint16(b[off:]), binary.LittleEndian.Uint32(b[off+2:])}
		off += 6
	}
	for i := 0; i < na; i++ {
		id, idx := binary.LittleEndian.Uint16(b[off:]), binary.LittleEndian.Uint16(b[off+2:])
		if int(idx) >= n {
			return nil, errors.New("an alias points past the entries")
		}
		p.aliases = append(p.aliases, [2]uint16{id, idx})
		off += 4
	}
	for i := 0; i < n; i++ {
		s, e := int(ents[i].off), int(ents[i+1].off)
		if s < off || e < s || e > len(b) {
			return nil, fmt.Errorf("resource %d has bad bounds", ents[i].id)
		}
		p.ids = append(p.ids, ents[i].id)
		p.data = append(p.data, b[s:e])
	}
	return p, nil
}

func (p *pak) bytes() []byte {
	n := len(p.ids)
	var buf bytes.Buffer
	hdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hdr, 5)
	hdr[4] = p.encoding
	binary.LittleEndian.PutUint16(hdr[8:], uint16(n))
	binary.LittleEndian.PutUint16(hdr[10:], uint16(len(p.aliases)))
	buf.Write(hdr)
	off := uint32(12 + (n+1)*6 + len(p.aliases)*4)
	e := make([]byte, 6)
	for i := 0; i <= n; i++ {
		id := uint16(0)
		if i < n {
			id = p.ids[i]
		}
		binary.LittleEndian.PutUint16(e, id)
		binary.LittleEndian.PutUint32(e[2:], off)
		buf.Write(e)
		if i < n {
			off += uint32(len(p.data[i]))
		}
	}
	a := make([]byte, 4)
	for _, al := range p.aliases {
		binary.LittleEndian.PutUint16(a, al[0])
		binary.LittleEndian.PutUint16(a[2:], al[1])
		buf.Write(a)
	}
	for _, d := range p.data {
		buf.Write(d)
	}
	return buf.Bytes()
}

// brotliMagic starts a resource grit compressed with Brotli; six bytes of
// little-endian uncompressed size follow, then the stream.
var brotliMagic = []byte{0x1e, 0x9b}

// resourceText answers a resource's content as ResourceBundle would load
// it (gzip or Brotli undone); ok is false when it cannot be decoded here.
func resourceText(d []byte, sizeHint int) ([]byte, bool) {
	switch {
	case bytes.HasPrefix(d, brotliMagic) && len(d) >= 8:
		size := int(binary.LittleEndian.Uint32(d[2:])) | int(binary.LittleEndian.Uint16(d[6:]))<<32
		if sizeHint >= 0 && size != sizeHint {
			return nil, false
		}
		out, err := brotliDecode(d[8:], size)
		return out, err == nil
	case bytes.HasPrefix(d, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(bytes.NewReader(d))
		if err != nil {
			return nil, false
		}
		out, err := io.ReadAll(io.LimitReader(zr, 64<<20))
		return out, err == nil
	}
	return d, true
}

// replaceResource swaps the resource whose content is exactly old for
// replacement (stored uncompressed), and refuses unless exactly one
// resource matches.
func replaceResource(packed, old, replacement []byte) ([]byte, uint16, error) {
	p, err := parsePak(packed)
	if err != nil {
		return nil, 0, err
	}
	if bytes.HasPrefix(replacement, brotliMagic) || bytes.HasPrefix(replacement, []byte{0x1f, 0x8b}) {
		return nil, 0, errors.New("the replacement would read as compressed data")
	}
	idx := -1
	for i, d := range p.data {
		if t, ok := resourceText(d, len(old)); ok && bytes.Equal(t, old) {
			if idx >= 0 {
				return nil, 0, fmt.Errorf("resources %d and %d both hold the placeholder", p.ids[idx], p.ids[i])
			}
			idx = i
		}
	}
	if idx < 0 {
		return nil, 0, errors.New("no resource holds the placeholder")
	}
	p.data[idx] = replacement
	return p.bytes(), p.ids[idx], nil
}
