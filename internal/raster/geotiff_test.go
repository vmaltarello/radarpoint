package raster

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/vmaltarello/radarpoint/internal/geo"
)

// testdata/ files are row bands of real Radar-DPC products
// (Radar-DPC – Dipartimento della Protezione Civile, CC-BY-SA 4.0), cut with
// cmd/tiffcrop, which copies the compressed strips unchanged:
//
//	sri_crop.tif   SRI 2026-10-01 12:45 UTC, rows 170-249 (LZW, Transverse Mercator)
//	temp_crop.tif  TEMP 2026-10-01 12:00 UTC, rows 90-101 (Deflate, EPSG:4326)

func TestSRICrop(t *testing.T) {
	g, err := OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	if g.Width != 1200 || g.Height != 80 || g.DataType() != "float32" || g.CompressionName() != "LZW" {
		t.Fatalf("layout %dx%d %s %s", g.Width, g.Height, g.DataType(), g.CompressionName())
	}
	tm, ok := g.Projection.(*geo.TransverseMercator)
	if !ok || tm.Lat0 != 42 || tm.Lon0 != 12.5 || tm.K0 != 1 || !tm.Ellipsoid.Near(geo.WGS84) {
		t.Fatalf("projection %v", g.Projection)
	}
	want := geo.GeoTransform{OriginX: -600000, OriginY: 480000, PixelW: 1000, PixelH: 1000}
	if g.Transform != want {
		t.Fatalf("transform %+v, want %+v", g.Transform, want)
	}

	// Pixel centres and values checked against the full original file.
	for _, c := range []struct {
		name     string
		lat, lon float64
		col, row int
		v        float64
	}{
		{"nodata", 46.02527, 4.75645, 0, 3, -9999},
		{"zero", 46.12731, 6.43761, 131, 3, 0},
		{"rain", 45.78886, 6.01149, 95, 38, 2.0199999809265137},
		{"Legnano", 45.5966, 8.9150, 320, 74, 0},
	} {
		v, col, row, err := g.ValueAt(c.lat, c.lon)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if col != c.col || row != c.row || v != c.v {
			t.Errorf("%s: pixel %d,%d value %v; want %d,%d %v", c.name, col, row, v, c.col, c.row, c.v)
		}
	}

	// Rome is inside the full grid but outside this 80-row band.
	if _, _, _, err := g.ValueAt(41.9, 12.5); !errors.Is(err, ErrOutside) {
		t.Errorf("Rome: err = %v, want ErrOutside", err)
	}
}

func TestTEMPCrop(t *testing.T) {
	g, err := OpenGeoTIFF("../../testdata/temp_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	if g.Width != 631 || g.Height != 12 || g.CompressionName() != "Deflate" {
		t.Fatalf("layout %dx%d %s", g.Width, g.Height, g.CompressionName())
	}
	if _, ok := g.Projection.(geo.Geographic); !ok {
		t.Fatalf("projection %v, want geographic", g.Projection)
	}
	for _, c := range []struct {
		name     string
		lat, lon float64
		col, row int
		v        float64
	}{
		{"Legnano", 45.5966, 8.9150, 145, 5, 22.898517608642578},
		{"Aosta valley", 45.6, 7.0, 50, 5, 6.9265594482421875},
		{"Adriatic (mask)", 45.6, 13.0, 350, 5, 0},
	} {
		v, col, row, err := g.ValueAt(c.lat, c.lon)
		if err != nil || col != c.col || row != c.row || v != c.v {
			t.Errorf("%s: pixel %d,%d value %v err %v; want %d,%d %v", c.name, col, row, v, err, c.col, c.row, c.v)
		}
	}
	if _, _, _, err := g.ValueAt(45.6, 5.9); !errors.Is(err, ErrOutside) {
		t.Errorf("west of grid: err = %v, want ErrOutside", err)
	}
}

func TestReadAllMatchesAt(t *testing.T) {
	g, err := OpenGeoTIFF("../../testdata/temp_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	all, err := g.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{0, 0}, {145, 5}, {630, 11}, {300, 7}} {
		v, err := g.At(p[0], p[1])
		if err != nil || v != all[p[1]*g.Width+p[0]] {
			t.Errorf("At%v = %v, %v; ReadAll %v", p, v, err, all[p[1]*g.Width+p[0]])
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"short", []byte("II*"), "too short"},
		{"bad order", []byte("XX*\x00\x08\x00\x00\x00"), "byte order"},
		{"bigtiff", []byte("II+\x00\x08\x00\x00\x00"), "BigTIFF"},
	} {
		if _, err := Parse(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestUndoFloatPredictor(t *testing.T) {
	// Encode two little-endian float32 with TIFF predictor 3, then decode.
	vals := []float32{1.5, -2.25}
	n, bps := len(vals), 4
	planes := make([]byte, n*bps)
	for s, v := range vals {
		b := math.Float32bits(v)
		for k := 0; k < bps; k++ { // most significant byte first
			planes[k*n+s] = byte(b >> (8 * (bps - 1 - k)))
		}
	}
	enc := make([]byte, len(planes))
	enc[0] = planes[0]
	for i := 1; i < len(planes); i++ {
		enc[i] = planes[i] - planes[i-1]
	}
	undoFloatPredictor(enc, bps, binary.LittleEndian)
	for s, v := range vals {
		got := math.Float32frombits(uint32(enc[4*s]) | uint32(enc[4*s+1])<<8 | uint32(enc[4*s+2])<<16 | uint32(enc[4*s+3])<<24)
		if got != v {
			t.Errorf("sample %d = %v, want %v", s, got, v)
		}
	}
}
