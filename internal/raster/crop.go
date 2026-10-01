package raster

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// CropRows returns a new TIFF holding rows [from, to) of a stripped image.
// Compressed strips are copied verbatim, so the result is decoded exactly like
// the original; from and to must therefore fall on strip boundaries (or to
// may be Height). The georeference is shifted to match. It is used to build
// small test files from real Radar-DPC products.
func (im *Image) CropRows(from, to int) ([]byte, error) {
	if im.tiled {
		return nil, errors.New("crop: tiled images not supported")
	}
	if from < 0 || to > im.Height || from >= to {
		return nil, fmt.Errorf("crop: bad row range %d-%d", from, to)
	}
	if from%im.blockH != 0 || (to%im.blockH != 0 && to != im.Height) {
		return nil, fmt.Errorf("crop: rows must be aligned to the %d-row strips", im.blockH)
	}
	o := im.order
	first, last := from/im.blockH, (to+im.blockH-1)/im.blockH

	var buf bytes.Buffer
	if o == binary.LittleEndian {
		buf.WriteString("II")
	} else {
		buf.WriteString("MM")
	}
	binary.Write(&buf, o, uint16(42))
	binary.Write(&buf, o, uint32(0)) // IFD offset, patched below

	var offs, counts []uint32
	for i := first; i < last; i++ {
		off, n := im.offsets[i], im.byteCounts[i]
		offs = append(offs, uint32(buf.Len()))
		counts = append(counts, uint32(n))
		buf.Write(im.data[off : off+n])
		if buf.Len()%2 == 1 {
			buf.WriteByte(0)
		}
	}

	longs := func(v []uint32) (uint16, uint32, []byte) {
		b := make([]byte, 4*len(v))
		for i, x := range v {
			o.PutUint32(b[4*i:], x)
		}
		return 4, uint32(len(v)), b
	}
	type entry struct {
		id, typ uint16
		count   uint32
		raw     []byte
	}
	var entries []entry
	for _, id := range im.SortedTagIDs() {
		t := im.Tags[id]
		e := entry{id, t.Type, t.Count, t.Raw}
		switch id {
		case TagImageLength:
			e.typ, e.count, e.raw = longs([]uint32{uint32(to - from)})
		case TagStripOffsets:
			e.typ, e.count, e.raw = longs(offs)
		case TagStripByteCounts:
			e.typ, e.count, e.raw = longs(counts)
		case TagModelTiepoint:
			if t.Type != 12 || len(t.Floats) < 6 {
				return nil, errors.New("crop: unexpected ModelTiepoint")
			}
			sc := im.Tags[TagModelPixelScale]
			if sc == nil || len(sc.Floats) < 2 {
				return nil, errors.New("crop: missing ModelPixelScale")
			}
			e.raw = append([]byte(nil), t.Raw...)
			o.PutUint64(e.raw[32:], math.Float64bits(t.Floats[4]-float64(from)*sc.Floats[1]))
		}
		entries = append(entries, e)
	}

	ifd := uint32(buf.Len())
	o.PutUint32(buf.Bytes()[4:], ifd)
	extra := ifd + 2 + 12*uint32(len(entries)) + 4
	var tail bytes.Buffer
	binary.Write(&buf, o, uint16(len(entries)))
	for _, e := range entries {
		binary.Write(&buf, o, e.id)
		binary.Write(&buf, o, e.typ)
		binary.Write(&buf, o, e.count)
		if len(e.raw) <= 4 {
			v := make([]byte, 4)
			copy(v, e.raw)
			buf.Write(v)
			continue
		}
		binary.Write(&buf, o, extra+uint32(tail.Len()))
		tail.Write(e.raw)
		if tail.Len()%2 == 1 {
			tail.WriteByte(0)
		}
	}
	binary.Write(&buf, o, uint32(0)) // no next IFD
	buf.Write(tail.Bytes())
	return buf.Bytes(), nil
}
