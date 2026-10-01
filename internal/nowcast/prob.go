package nowcast

import "math"

// The probability of rain at a point is the share of pixels with rain in a
// square around the place the rain comes from. The square grows with lead
// time, since the position error of an extrapolation grows too.

// probRadius is the half side of the square at step k, in pixels.
func (n *Nowcast) probRadius(k int) int {
	return int(math.Round(n.options.ProbRadius + n.options.ProbRadiusPerStep*float64(k)))
}

// probAt is the share of rain pixels (≥ ProbThreshold) among the pixels
// with data in the square of half side r around (x, y), or NaN if fewer
// than half of them have data.
func (n *Nowcast) probAt(x, y, r int) float64 {
	f := n.Latest
	thr := n.options.ProbThreshold
	var wet, valid, all int
	for yy := y - r; yy <= y+r; yy++ {
		for xx := x - r; xx <= x+r; xx++ {
			all++
			if xx < 0 || yy < 0 || xx >= f.W || yy >= f.H {
				continue
			}
			if v := f.V[yy*f.W+xx]; v == v {
				valid++
				if v >= thr {
					wet++
				}
			}
		}
	}
	if 2*valid < all {
		return math.NaN()
	}
	return float64(wet) / float64(valid)
}

// integral holds summed-area tables of the rain and data masks of a field,
// to count pixels in any square in constant time.
type integral struct {
	w, h     int
	wet, val []int32 // (w+1)×(h+1), row-major
}

func newIntegral(f *Field, thr float32) *integral {
	w, h := f.W, f.H
	in := &integral{w: w, h: h, wet: make([]int32, (w+1)*(h+1)), val: make([]int32, (w+1)*(h+1))}
	for y := range h {
		var rowWet, rowVal int32
		for x := range w {
			if v := f.V[y*w+x]; v == v {
				rowVal++
				if v >= thr {
					rowWet++
				}
			}
			i := (y+1)*(w+1) + x + 1
			in.wet[i] = in.wet[i-(w+1)] + rowWet
			in.val[i] = in.val[i-(w+1)] + rowVal
		}
	}
	return in
}

// prob matches probAt for the square of half side r around (x, y).
func (in *integral) prob(x, y, r int) float32 {
	if x < 0 || y < 0 || x >= in.w || y >= in.h {
		return float32(math.NaN())
	}
	x0, y0 := max(x-r, 0), max(y-r, 0)
	x1, y1 := min(x+r+1, in.w), min(y+r+1, in.h)
	sum := func(t []int32) int32 {
		W := in.w + 1
		return t[y1*W+x1] - t[y0*W+x1] - t[y1*W+x0] + t[y0*W+x0]
	}
	valid := sum(in.val)
	side := 2*r + 1
	if 2*int(valid) < side*side {
		return float32(math.NaN())
	}
	return float32(sum(in.wet)) / float32(valid)
}

// ProbabilityFields returns, for steps 1…steps, the probability of rain
// (0–1) over the whole grid. It is meant for verification and maps; point
// queries get it in Forecast.
func (n *Nowcast) ProbabilityFields(steps int) []*Field {
	w, h := n.Latest.W, n.Latest.H
	in := newIntegral(n.Latest, n.options.ProbThreshold)
	out := make([]*Field, steps)
	for k := range out {
		out[k] = &Field{W: w, H: h, V: make([]float32, w*h)}
	}
	parallelRows(h, func(row int) {
		for col := range w {
			x, y := float64(col)+0.5, float64(row)+0.5
			for k := range steps {
				x, y = n.stepBack(x, y)
				out[k].V[row*w+col] = in.prob(int(math.Floor(x)), int(math.Floor(y)), n.probRadius(k+1))
			}
		}
	})
	return out
}
