package geo

import (
	"math"
	"testing"
)

// dpcTM is the projection declared by Radar-DPC SRI/POH files.
func dpcTM() *TransverseMercator {
	return &TransverseMercator{Lat0: 42, Lon0: 12.5, K0: 1, Ellipsoid: WGS84}
}

// Snyder, "Map Projections – A Working Manual" (USGS PP 1395, 1987), p. 269:
// ellipsoidal Transverse Mercator numerical example on Clarke 1866.
func TestTransverseMercatorSnyderExample(t *testing.T) {
	e2 := 0.00676866
	p := &TransverseMercator{Lat0: 0, Lon0: -75, K0: 0.9996,
		Ellipsoid: Ellipsoid{A: 6378206.4, F: 1 - math.Sqrt(1-e2)}}
	x, y := p.Forward(40.5, -73.5)
	if math.Abs(x-127106.5) > 0.5 || math.Abs(y-4484124.4) > 0.5 {
		t.Fatalf("Forward = %.1f, %.1f; want 127106.5, 4484124.4", x, y)
	}
}

func TestTransverseMercatorOrigin(t *testing.T) {
	x, y := dpcTM().Forward(42, 12.5)
	if math.Abs(x) > 1e-6 || math.Abs(y) > 1e-6 {
		t.Fatalf("origin projects to %g, %g; want 0, 0", x, y)
	}
	// On the central meridian, northing is the meridian arc from 42°N:
	// 1° of latitude at ~42.5°N is about 111.07 km.
	_, y = dpcTM().Forward(43, 12.5)
	if math.Abs(y-111067) > 50 {
		t.Fatalf("northing of 43N = %.0f; want about 111067", y)
	}
}

func TestTransverseMercatorRoundTrip(t *testing.T) {
	p := dpcTM()
	for _, pt := range [][2]float64{
		{45.5966, 8.9150}, // Legnano
		{35.5, 12.6},      // Lampedusa
		{47.0, 5.0},       // NW corner of the grid, 7.5° off the meridian
		{39.2, 9.1},       // Cagliari
		{40.6, 18.0},      // Salento
	} {
		x, y := p.Forward(pt[0], pt[1])
		lat, lon := p.Inverse(x, y)
		if math.Abs(lat-pt[0]) > 1e-9 || math.Abs(lon-pt[1]) > 1e-9 {
			t.Errorf("round trip %v → (%.3f, %.3f) → (%.10f, %.10f)", pt, x, y, lat, lon)
		}
	}
}

func TestGeographic(t *testing.T) {
	x, y := Geographic{}.Forward(45.5, 9.1)
	if x != 9.1 || y != 45.5 {
		t.Fatalf("Forward = %g, %g", x, y)
	}
}

func TestGeoTransformPixel(t *testing.T) {
	// Radar-DPC 1 km grid: top-left corner at (-600000, 650000).
	g := GeoTransform{OriginX: -600000, OriginY: 650000, PixelW: 1000, PixelH: 1000}
	for _, c := range []struct {
		x, y     float64
		col, row int
	}{
		{-600000, 650000, 0, 0},
		{-599000.1, 649000.1, 0, 0},
		{0, 0, 600, 650},
		{599999, -749999, 1199, 1399},
		{-600001, 650000, -1, 0},
	} {
		col, row := g.Pixel(c.x, c.y)
		if col != c.col || row != c.row {
			t.Errorf("Pixel(%g, %g) = %d, %d; want %d, %d", c.x, c.y, col, row, c.col, c.row)
		}
	}
	if x, y := g.Center(600, 650); x != 500 || y != -500 {
		t.Errorf("Center(600, 650) = %g, %g", x, y)
	}
}
