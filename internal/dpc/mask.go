package dpc

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"math"
)

// Mask marks the pixels of a product grid that have no data by design,
// such as sea and foreign countries for TEMP.
type Mask struct {
	W, H             int
	OriginX, OriginY float64 // top-left corner of the grid
	Pixel            float64 // pixel size
	outside          []bool
}

// Grid describes a raster grid, to check that a mask applies to it.
type Grid struct {
	W, H             int
	OriginX, OriginY float64
	PixelW           float64
}

// Matches reports whether the mask was built for grid g.
func (m *Mask) Matches(g Grid) bool {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	return m != nil && m.W == g.W && m.H == g.H && near(m.OriginX, g.OriginX) && near(m.OriginY, g.OriginY) && near(m.Pixel, g.PixelW)
}

// Outside reports whether pixel (col, row) has no data by design.
func (m *Mask) Outside(col, row int) bool {
	if col < 0 || row < 0 || col >= m.W || row >= m.H {
		return true
	}
	return m.outside[row*m.W+col]
}

// The TEMP mask is built by cmd/tempmask from two weeks of files: white
// pixels were exactly 0 in all of them (sea, outside Italy, San Marino and
// Vatican City).
//
//go:embed tempmask.png
var tempMaskPNG []byte

var tempMask = loadMask(tempMaskPNG, 6, 47.50026321411133, 0.019983009747110096)

func loadMask(b []byte, originX, originY, pixel float64) *Mask {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		panic("dpc: embedded mask: " + err.Error())
	}
	r := img.Bounds()
	m := &Mask{W: r.Dx(), H: r.Dy(), OriginX: originX, OriginY: originY, Pixel: pixel, outside: make([]bool, r.Dx()*r.Dy())}
	p, ok := img.(*image.Paletted)
	for y := range m.H {
		for x := range m.W {
			var white bool
			if ok {
				white = p.ColorIndexAt(x, y) == 1
			} else {
				g, _, _, _ := img.At(x, y).RGBA()
				white = g > 0x8000
			}
			m.outside[y*m.W+x] = white
		}
	}
	return m
}
