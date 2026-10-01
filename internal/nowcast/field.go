// Package nowcast forecasts rain for the next hour by extrapolating the
// motion of the latest radar images (Lagrangian persistence).
//
// The motion is estimated by block matching between consecutive frames: each
// block of the newer frame is compared with shifted copies of the older one,
// and the shift with the smallest difference is the displacement of the rain
// in that block. Forecasts then follow the motion field backwards from the
// point of interest and read the latest observation where the rain comes from.
//
// Rain is only moved, never created, grown or dissipated, so the forecast
// cannot anticipate new storms and gets less reliable with lead time.
package nowcast

import (
	"math"

	"github.com/vmaltarello/radarpoint/internal/raster"
)

// Field is a decoded raster with nodata stored as NaN.
type Field struct {
	W, H int
	V    []float32 // row-major, W*H values
}

// NewField decodes a GeoTIFF, replacing values for which isNoData is true
// with NaN.
func NewField(g *raster.GeoTIFF, isNoData func(float64) bool) (*Field, error) {
	vals, err := g.ReadAll()
	if err != nil {
		return nil, err
	}
	f := &Field{W: g.Width, H: g.Height, V: make([]float32, len(vals))}
	for i, v := range vals {
		if isNoData(v) {
			f.V[i] = float32(math.NaN())
		} else {
			f.V[i] = float32(v)
		}
	}
	return f, nil
}

// At returns the value at pixel (x, y), or NaN outside the grid.
func (f *Field) At(x, y int) float32 {
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return float32(math.NaN())
	}
	return f.V[y*f.W+x]
}

// downsample averages factor×factor pixels, ignoring NaN. A cell is NaN only
// if all its pixels are.
func (f *Field) downsample(factor int) *Field {
	w, h := f.W/factor, f.H/factor
	out := &Field{W: w, H: h, V: make([]float32, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float32
			n := 0
			for dy := 0; dy < factor; dy++ {
				row := (y*factor + dy) * f.W
				for dx := 0; dx < factor; dx++ {
					if v := f.V[row+x*factor+dx]; v == v { // not NaN
						sum += v
						n++
					}
				}
			}
			if n == 0 {
				out.V[y*w+x] = float32(math.NaN())
			} else {
				out.V[y*w+x] = sum / float32(n)
			}
		}
	}
	return out
}
