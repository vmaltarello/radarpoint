// Package raster reads single-band GeoTIFF files as produced by Radar-DPC.
//
// It is a minimal, pure Go TIFF reader: classic TIFF (not BigTIFF), strips or
// tiles, no compression / LZW / Deflate, predictors 1, 2 and 3, chunky
// planar configuration, integer and floating point samples.
package raster

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/tiff/lzw"
)

// TIFF tag IDs used by this package.
const (
	TagImageWidth          = 256
	TagImageLength         = 257
	TagBitsPerSample       = 258
	TagCompression         = 259
	TagStripOffsets        = 273
	TagSamplesPerPixel     = 277
	TagRowsPerStrip        = 278
	TagStripByteCounts     = 279
	TagPlanarConfiguration = 284
	TagPredictor           = 317
	TagTileWidth           = 322
	TagTileLength          = 323
	TagTileOffsets         = 324
	TagTileByteCounts      = 325
	TagSampleFormat        = 339
	TagModelPixelScale     = 33550
	TagModelTiepoint       = 33922
	TagModelTransformation = 34264
	TagGeoKeyDirectory     = 34735
	TagGeoDoubleParams     = 34736
	TagGeoAsciiParams      = 34737
	TagGDALMetadata        = 42112
	TagGDALNoData          = 42113
)

// Compression values.
const (
	CompressionNone    = 1
	CompressionLZW     = 5
	CompressionDeflate = 8
	CompressionAdobe   = 32946 // old-style Deflate code
)

// Tag is a decoded TIFF tag. Exactly one of Ints, Floats, Str is meaningful,
// depending on the TIFF field type.
type Tag struct {
	ID     uint16
	Type   uint16
	Count  uint32
	Ints   []int64
	Floats []float64
	Str    string
	Raw    []byte // value bytes as stored, in the file byte order
}

// Image is a parsed TIFF (first IFD only) with its raw bytes kept in memory.
type Image struct {
	data  []byte
	order binary.ByteOrder

	Tags map[uint16]*Tag

	Width, Height   int
	BitsPerSample   int
	SampleFormat    int // 1 uint, 2 int, 3 float
	SamplesPerPixel int
	Compression     int
	Predictor       int

	tiled               bool
	blockW, blockH      int
	offsets, byteCounts []int64
	blocksAcross        int
	bytesPerSample      int
}

// Open reads and parses a TIFF file.
func Open(path string) (*Image, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse parses a TIFF from memory.
func Parse(b []byte) (*Image, error) {
	if len(b) < 8 {
		return nil, errors.New("tiff: file too short")
	}
	im := &Image{data: b, Tags: map[uint16]*Tag{}}
	switch string(b[:2]) {
	case "II":
		im.order = binary.LittleEndian
	case "MM":
		im.order = binary.BigEndian
	default:
		return nil, errors.New("tiff: bad byte order marker")
	}
	switch im.order.Uint16(b[2:4]) {
	case 42:
	case 43:
		return nil, errors.New("tiff: BigTIFF not supported")
	default:
		return nil, errors.New("tiff: bad magic number")
	}
	ifd := int64(im.order.Uint32(b[4:8]))
	if err := im.readIFD(ifd); err != nil {
		return nil, err
	}
	if err := im.setup(); err != nil {
		return nil, err
	}
	return im, nil
}

var typeSize = map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 16: 8, 17: 8}

func (im *Image) readIFD(off int64) error {
	b := im.data
	if off+2 > int64(len(b)) {
		return errors.New("tiff: IFD offset out of range")
	}
	n := int64(im.order.Uint16(b[off:]))
	if off+2+n*12 > int64(len(b)) {
		return errors.New("tiff: IFD truncated")
	}
	for i := int64(0); i < n; i++ {
		e := b[off+2+i*12 : off+2+(i+1)*12]
		t := &Tag{ID: im.order.Uint16(e[0:]), Type: im.order.Uint16(e[2:]), Count: im.order.Uint32(e[4:])}
		sz, ok := typeSize[t.Type]
		if !ok {
			continue // unknown type: skip per spec
		}
		total := int64(sz) * int64(t.Count)
		var v []byte
		if total <= 4 {
			v = e[8 : 8+total]
		} else {
			p := int64(im.order.Uint32(e[8:]))
			if p < 0 || p+total > int64(len(b)) {
				return fmt.Errorf("tiff: tag %d value out of range", t.ID)
			}
			v = b[p : p+total]
		}
		t.Raw = v
		im.decodeTag(t, v)
		im.Tags[t.ID] = t
	}
	return nil
}

func (im *Image) decodeTag(t *Tag, v []byte) {
	o := im.order
	n := int(t.Count)
	switch t.Type {
	case 2: // ASCII
		t.Str = strings.TrimRight(string(v), "\x00")
	case 1, 7:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(v[i]))
		}
	case 6:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(int8(v[i])))
		}
	case 3:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(o.Uint16(v[2*i:])))
		}
	case 8:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(int16(o.Uint16(v[2*i:]))))
		}
	case 4:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(o.Uint32(v[4*i:])))
		}
	case 9:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(int32(o.Uint32(v[4*i:]))))
		}
	case 16, 17:
		for i := 0; i < n; i++ {
			t.Ints = append(t.Ints, int64(o.Uint64(v[8*i:])))
		}
	case 5:
		for i := 0; i < n; i++ {
			t.Floats = append(t.Floats, float64(o.Uint32(v[8*i:]))/float64(o.Uint32(v[8*i+4:])))
		}
	case 10:
		for i := 0; i < n; i++ {
			t.Floats = append(t.Floats, float64(int32(o.Uint32(v[8*i:])))/float64(int32(o.Uint32(v[8*i+4:]))))
		}
	case 11:
		for i := 0; i < n; i++ {
			t.Floats = append(t.Floats, float64(math.Float32frombits(o.Uint32(v[4*i:]))))
		}
	case 12:
		for i := 0; i < n; i++ {
			t.Floats = append(t.Floats, math.Float64frombits(o.Uint64(v[8*i:])))
		}
	}
}

func (im *Image) intTag(id uint16, def int) int {
	if t, ok := im.Tags[id]; ok && len(t.Ints) > 0 {
		return int(t.Ints[0])
	}
	return def
}

func (im *Image) ints(id uint16) []int64 {
	if t, ok := im.Tags[id]; ok {
		return t.Ints
	}
	return nil
}

func (im *Image) setup() error {
	im.Width = im.intTag(TagImageWidth, 0)
	im.Height = im.intTag(TagImageLength, 0)
	im.BitsPerSample = im.intTag(TagBitsPerSample, 1)
	im.SampleFormat = im.intTag(TagSampleFormat, 1)
	im.SamplesPerPixel = im.intTag(TagSamplesPerPixel, 1)
	im.Compression = im.intTag(TagCompression, CompressionNone)
	im.Predictor = im.intTag(TagPredictor, 1)
	if im.Width <= 0 || im.Height <= 0 {
		return errors.New("tiff: missing image dimensions")
	}
	if im.SamplesPerPixel != 1 {
		return fmt.Errorf("tiff: %d samples per pixel not supported (single band only)", im.SamplesPerPixel)
	}
	if im.intTag(TagPlanarConfiguration, 1) != 1 && im.SamplesPerPixel > 1 {
		return errors.New("tiff: planar configuration 2 not supported")
	}
	switch im.BitsPerSample {
	case 8, 16, 32, 64:
		im.bytesPerSample = im.BitsPerSample / 8
	default:
		return fmt.Errorf("tiff: %d bits per sample not supported", im.BitsPerSample)
	}
	if im.SampleFormat == 3 && im.bytesPerSample < 4 {
		return errors.New("tiff: half-precision floats not supported")
	}
	switch im.Compression {
	case CompressionNone, CompressionLZW, CompressionDeflate, CompressionAdobe:
	default:
		return fmt.Errorf("tiff: compression %d not supported", im.Compression)
	}
	if _, ok := im.Tags[TagTileWidth]; ok {
		im.tiled = true
		im.blockW = im.intTag(TagTileWidth, 0)
		im.blockH = im.intTag(TagTileLength, 0)
		im.offsets = im.ints(TagTileOffsets)
		im.byteCounts = im.ints(TagTileByteCounts)
	} else {
		im.blockW = im.Width
		im.blockH = im.intTag(TagRowsPerStrip, im.Height)
		if im.blockH > im.Height {
			im.blockH = im.Height
		}
		im.offsets = im.ints(TagStripOffsets)
		im.byteCounts = im.ints(TagStripByteCounts)
	}
	if im.blockW <= 0 || im.blockH <= 0 {
		return errors.New("tiff: bad strip/tile size")
	}
	im.blocksAcross = (im.Width + im.blockW - 1) / im.blockW
	blocksDown := (im.Height + im.blockH - 1) / im.blockH
	if len(im.offsets) < im.blocksAcross*blocksDown || len(im.byteCounts) < len(im.offsets) {
		return errors.New("tiff: missing strip/tile offsets")
	}
	return nil
}

// Tiled reports whether the image uses tiles rather than strips.
func (im *Image) Tiled() bool { return im.tiled }

// BlockSize returns the strip/tile width and height in pixels.
func (im *Image) BlockSize() (w, h int) { return im.blockW, im.blockH }

// decodeBlock returns the uncompressed, un-predicted bytes of block i,
// laid out as blockW*rows samples in the file's byte order.
func (im *Image) decodeBlock(i int) ([]byte, error) {
	off, n := im.offsets[i], im.byteCounts[i]
	if off < 0 || off+n > int64(len(im.data)) {
		return nil, fmt.Errorf("tiff: block %d out of range", i)
	}
	raw := im.data[off : off+n]
	rows := im.blockH
	if !im.tiled {
		if r := im.Height - (i * im.blockH); r < rows {
			rows = r
		}
	}
	want := im.blockW * rows * im.bytesPerSample

	var out []byte
	switch im.Compression {
	case CompressionNone:
		out = raw
	case CompressionLZW:
		r := lzw.NewReader(bytes.NewReader(raw), lzw.MSB, 8)
		var err error
		out, err = io.ReadAll(io.LimitReader(r, int64(want)))
		r.Close()
		if err != nil && len(out) < want {
			return nil, fmt.Errorf("tiff: LZW block %d: %w", i, err)
		}
	case CompressionDeflate, CompressionAdobe:
		r, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("tiff: deflate block %d: %w", i, err)
		}
		out, err = io.ReadAll(io.LimitReader(r, int64(want)))
		r.Close()
		if err != nil {
			return nil, fmt.Errorf("tiff: deflate block %d: %w", i, err)
		}
	}
	if len(out) < want {
		return nil, fmt.Errorf("tiff: block %d short: %d of %d bytes", i, len(out), want)
	}
	out = out[:want]
	if im.Compression == CompressionNone {
		out = append([]byte(nil), out...)
	}
	rowBytes := im.blockW * im.bytesPerSample
	switch im.Predictor {
	case 1:
	case 2:
		for r := 0; r < rows; r++ {
			undoHorizontal(out[r*rowBytes:(r+1)*rowBytes], im.bytesPerSample, im.order)
		}
	case 3:
		for r := 0; r < rows; r++ {
			undoFloatPredictor(out[r*rowBytes:(r+1)*rowBytes], im.bytesPerSample, im.order)
		}
	default:
		return nil, fmt.Errorf("tiff: predictor %d not supported", im.Predictor)
	}
	return out, nil
}

func undoHorizontal(row []byte, bps int, o binary.ByteOrder) {
	switch bps {
	case 1:
		for i := 1; i < len(row); i++ {
			row[i] += row[i-1]
		}
	case 2:
		for i := 2; i < len(row); i += 2 {
			o.PutUint16(row[i:], o.Uint16(row[i:])+o.Uint16(row[i-2:]))
		}
	case 4:
		for i := 4; i < len(row); i += 4 {
			o.PutUint32(row[i:], o.Uint32(row[i:])+o.Uint32(row[i-4:]))
		}
	case 8:
		for i := 8; i < len(row); i += 8 {
			o.PutUint64(row[i:], o.Uint64(row[i:])+o.Uint64(row[i-8:]))
		}
	}
}

// undoFloatPredictor reverses TIFF predictor 3: byte-wise differencing over
// the row, with bytes of each sample split into planes, most significant first.
func undoFloatPredictor(row []byte, bps int, o binary.ByteOrder) {
	for i := 1; i < len(row); i++ {
		row[i] += row[i-1]
	}
	n := len(row) / bps
	tmp := make([]byte, len(row))
	for s := 0; s < n; s++ {
		for b := 0; b < bps; b++ {
			// plane b holds byte b of each sample in big-endian significance
			v := row[b*n+s]
			if o == binary.LittleEndian {
				tmp[s*bps+(bps-1-b)] = v
			} else {
				tmp[s*bps+b] = v
			}
		}
	}
	copy(row, tmp)
}

func (im *Image) sample(buf []byte, idx int) float64 {
	p := buf[idx*im.bytesPerSample:]
	o := im.order
	switch im.SampleFormat {
	case 3:
		if im.bytesPerSample == 4 {
			return float64(math.Float32frombits(o.Uint32(p)))
		}
		return math.Float64frombits(o.Uint64(p))
	case 2:
		switch im.bytesPerSample {
		case 1:
			return float64(int8(p[0]))
		case 2:
			return float64(int16(o.Uint16(p)))
		case 4:
			return float64(int32(o.Uint32(p)))
		default:
			return float64(int64(o.Uint64(p)))
		}
	default:
		switch im.bytesPerSample {
		case 1:
			return float64(p[0])
		case 2:
			return float64(o.Uint16(p))
		case 4:
			return float64(o.Uint32(p))
		default:
			return float64(o.Uint64(p))
		}
	}
}

// At returns the value at pixel (x, y), decoding only the block containing it.
func (im *Image) At(x, y int) (float64, error) {
	if x < 0 || y < 0 || x >= im.Width || y >= im.Height {
		return 0, fmt.Errorf("pixel (%d,%d) outside %dx%d raster", x, y, im.Width, im.Height)
	}
	bx, by := x/im.blockW, y/im.blockH
	buf, err := im.decodeBlock(by*im.blocksAcross + bx)
	if err != nil {
		return 0, err
	}
	return im.sample(buf, (y%im.blockH)*im.blockW+(x%im.blockW)), nil
}

// ReadAll decodes the whole raster into a row-major slice of Width*Height values.
func (im *Image) ReadAll() ([]float64, error) {
	out := make([]float64, im.Width*im.Height)
	blocksDown := (im.Height + im.blockH - 1) / im.blockH
	for by := 0; by < blocksDown; by++ {
		for bx := 0; bx < im.blocksAcross; bx++ {
			buf, err := im.decodeBlock(by*im.blocksAcross + bx)
			if err != nil {
				return nil, err
			}
			for r := 0; r < im.blockH; r++ {
				y := by*im.blockH + r
				if y >= im.Height {
					break
				}
				for c := 0; c < im.blockW; c++ {
					x := bx*im.blockW + c
					if x >= im.Width {
						break
					}
					out[y*im.Width+x] = im.sample(buf, r*im.blockW+c)
				}
			}
		}
	}
	return out, nil
}

// NoData returns the GDAL_NODATA value, if present.
func (im *Image) NoData() (float64, bool) {
	t, ok := im.Tags[TagGDALNoData]
	if !ok {
		return 0, false
	}
	s := strings.TrimSpace(t.Str)
	if strings.EqualFold(s, "nan") {
		return math.NaN(), true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// SortedTagIDs returns the tag IDs in ascending order.
func (im *Image) SortedTagIDs() []uint16 {
	ids := make([]uint16, 0, len(im.Tags))
	for id := range im.Tags {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
