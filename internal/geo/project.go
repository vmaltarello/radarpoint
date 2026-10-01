// Package geo converts geographic coordinates to raster pixels.
//
// It implements only what Radar-DPC files need: plain geographic (EPSG:4326)
// grids and an ellipsoidal Transverse Mercator projection.
package geo

import (
	"fmt"
	"math"
)

// Ellipsoid is defined by its semi-major axis (metres) and flattening.
type Ellipsoid struct {
	A, F float64
}

// WGS84 is the ellipsoid used by every Radar-DPC product inspected so far.
var WGS84 = Ellipsoid{A: 6378137, F: 1 / 298.257223563}

// Near reports whether two ellipsoids are equal within rounding of their
// parameters, e.g. a flattening derived from a stored inverse flattening.
func (e Ellipsoid) Near(o Ellipsoid) bool {
	return math.Abs(e.A-o.A) < 1e-3 && math.Abs(e.F-o.F) < 1e-12
}

// Projection maps latitude/longitude (degrees) to model coordinates.
type Projection interface {
	Forward(lat, lon float64) (x, y float64)
	Inverse(x, y float64) (lat, lon float64)
	String() string
}

// Geographic is the identity projection: x = longitude, y = latitude.
type Geographic struct {
	Ellipsoid Ellipsoid
}

func (Geographic) Forward(lat, lon float64) (float64, float64) { return lon, lat }
func (Geographic) Inverse(x, y float64) (float64, float64)     { return y, x }
func (Geographic) String() string                              { return "geographic lat/lon WGS84 (EPSG:4326)" }

// TransverseMercator is the ellipsoidal Transverse Mercator projection,
// computed with Krüger's series in n to the fourth order (Karney 2011),
// accurate to well under a millimetre within a few thousand km of the
// central meridian.
type TransverseMercator struct {
	Lat0, Lon0 float64 // natural origin, degrees
	K0         float64 // scale factor at the central meridian
	FE, FN     float64 // false easting/northing, metres
	Ellipsoid  Ellipsoid

	ready       bool
	e, a        float64 // eccentricity, rectifying radius
	alpha, beta [4]float64
	delta       [4]float64
	xi0         float64 // rectifying latitude of Lat0 (radians)
}

func (p *TransverseMercator) init() {
	if p.ready {
		return
	}
	f := p.Ellipsoid.F
	n := f / (2 - f)
	n2, n3, n4 := n*n, n*n*n, n*n*n*n
	p.e = math.Sqrt(f * (2 - f))
	p.a = p.Ellipsoid.A / (1 + n) * (1 + n2/4 + n4/64)
	p.alpha = [4]float64{
		n/2 - 2*n2/3 + 5*n3/16 + 41*n4/180,
		13*n2/48 - 3*n3/5 + 557*n4/1440,
		61*n3/240 - 103*n4/140,
		49561 * n4 / 161280,
	}
	p.beta = [4]float64{
		n/2 - 2*n2/3 + 37*n3/96 - n4/360,
		n2/48 + n3/15 - 437*n4/1440,
		17*n3/480 - 37*n4/840,
		4397 * n4 / 161280,
	}
	p.delta = [4]float64{
		2*n - 2*n2/3 - 2*n3 + 116*n4/45,
		7*n2/3 - 8*n3/5 - 227*n4/45,
		56*n3/15 - 136*n4/35,
		4279 * n4 / 630,
	}
	p.ready = true
	p.xi0, _ = p.xiEta(rad(p.Lat0), 0)
}

// xiEta returns the normalised TM coordinates of a point, with lon relative
// to the central meridian (radians).
func (p *TransverseMercator) xiEta(phi, lam float64) (xi, eta float64) {
	s := math.Sin(phi)
	t := math.Sinh(math.Atanh(s) - p.e*math.Atanh(p.e*s))
	xi1 := math.Atan2(t, math.Cos(lam))
	eta1 := math.Atanh(math.Sin(lam) / math.Sqrt(1+t*t))
	xi, eta = xi1, eta1
	for j, a := range p.alpha {
		k := float64(2 * (j + 1))
		xi += a * math.Sin(k*xi1) * math.Cosh(k*eta1)
		eta += a * math.Cos(k*xi1) * math.Sinh(k*eta1)
	}
	return xi, eta
}

// Forward projects lat/lon (degrees) to easting/northing (metres).
func (p *TransverseMercator) Forward(lat, lon float64) (x, y float64) {
	p.init()
	xi, eta := p.xiEta(rad(lat), rad(lon-p.Lon0))
	ka := p.K0 * p.a
	return p.FE + ka*eta, p.FN + ka*(xi-p.xi0)
}

// Inverse converts easting/northing (metres) back to lat/lon (degrees).
func (p *TransverseMercator) Inverse(x, y float64) (lat, lon float64) {
	p.init()
	ka := p.K0 * p.a
	xi := (y-p.FN)/ka + p.xi0
	eta := (x - p.FE) / ka
	xi1, eta1 := xi, eta
	for j, b := range p.beta {
		k := float64(2 * (j + 1))
		xi1 -= b * math.Sin(k*xi) * math.Cosh(k*eta)
		eta1 -= b * math.Cos(k*xi) * math.Sinh(k*eta)
	}
	chi := math.Asin(math.Sin(xi1) / math.Cosh(eta1))
	phi := chi
	for j, d := range p.delta {
		phi += d * math.Sin(float64(2*(j+1))*chi)
	}
	return deg(phi), p.Lon0 + deg(math.Atan2(math.Sinh(eta1), math.Cos(xi1)))
}

func (p *TransverseMercator) String() string {
	name := "Transverse Mercator"
	if p.Ellipsoid.Near(WGS84) {
		name += " WGS84"
	}
	return fmt.Sprintf("%s (lat0=%g, lon0=%g, k0=%g, FE=%g, FN=%g)", name, p.Lat0, p.Lon0, p.K0, p.FE, p.FN)
}

// GeoTransform is an affine, north-up raster georeference: model coordinates
// of the top-left corner of pixel (0,0) and the pixel size. PixelH is
// positive and rows grow southwards.
type GeoTransform struct {
	OriginX, OriginY float64
	PixelW, PixelH   float64
}

// Pixel returns the column and row containing model point (x, y). The result
// may be outside the raster; the caller checks the bounds.
func (g GeoTransform) Pixel(x, y float64) (col, row int) {
	return int(math.Floor((x - g.OriginX) / g.PixelW)), int(math.Floor((g.OriginY - y) / g.PixelH))
}

// Center returns the model coordinates of the centre of pixel (col, row).
func (g GeoTransform) Center(col, row int) (x, y float64) {
	return g.OriginX + (float64(col)+0.5)*g.PixelW, g.OriginY - (float64(row)+0.5)*g.PixelH
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }
