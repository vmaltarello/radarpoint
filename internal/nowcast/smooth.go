package nowcast

import (
	"math"
	"runtime"
	"sync"
)

// minMass is the share of the Gaussian weight that must fall on pixels with
// data for a smoothed value to be defined; below it the result is NaN, so
// rain is not extrapolated out of thin coverage.
const minMass = 0.3

// kernel returns the 1-D Gaussian weights for offsets -r…r, and r.
func kernel(sigma float64) ([]float64, int) {
	r := int(math.Ceil(2.5 * sigma))
	k := make([]float64, 2*r+1)
	for d := -r; d <= r; d++ {
		k[d+r] = math.Exp(-float64(d*d) / (2 * sigma * sigma))
	}
	return k, r
}

// smoothAt is the Gaussian-weighted mean of f around pixel (x, y), ignoring
// NaN, or NaN where too little of the weight has data. It matches blur
// exactly, pixel by pixel.
func smoothAt(f *Field, x, y int, sigma float64) float32 {
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return float32(math.NaN())
	}
	k, r := kernel(sigma)
	var num, den, total float64
	for dy := -r; dy <= r; dy++ {
		var hn, hd, ht float64
		yy := y + dy
		for dx := -r; dx <= r; dx++ {
			w := k[dx+r]
			ht += w
			xx := x + dx
			if xx < 0 || yy < 0 || xx >= f.W || yy >= f.H {
				continue
			}
			if v := f.V[yy*f.W+xx]; v == v {
				hn += w * float64(v)
				hd += w
			}
		}
		w := k[dy+r]
		num += w * hn
		den += w * hd
		total += w * ht
	}
	if den < minMass*total {
		return float32(math.NaN())
	}
	return float32(num / den)
}

// blur applies smoothAt to every pixel, with two separable passes.
func blur(f *Field, sigma float64) *Field {
	k, r := kernel(sigma)
	w, h := f.W, f.H
	hn, hd := make([]float64, w*h), make([]float64, w*h)
	parallelRows(h, func(y int) {
		for x := range w {
			var n, d float64
			for dx := -r; dx <= r; dx++ {
				xx := x + dx
				if xx < 0 || xx >= w {
					continue
				}
				if v := f.V[y*w+xx]; v == v {
					n += k[dx+r] * float64(v)
					d += k[dx+r]
				}
			}
			hn[y*w+x], hd[y*w+x] = n, d
		}
	})
	var sum float64
	for _, v := range k {
		sum += v
	}
	total := sum * sum
	out := &Field{W: w, H: h, V: make([]float32, w*h)}
	parallelRows(h, func(y int) {
		for x := range w {
			var n, d float64
			for dy := -r; dy <= r; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				n += k[dy+r] * hn[yy*w+x]
				d += k[dy+r] * hd[yy*w+x]
			}
			if d < minMass*total {
				out.V[y*w+x] = float32(math.NaN())
			} else {
				out.V[y*w+x] = float32(n / d)
			}
		}
	})
	return out
}

// parallelRows calls f for every row in 0…h-1, spread over the CPUs.
func parallelRows(h int, f func(y int)) {
	var wg sync.WaitGroup
	rows := make(chan int, h)
	for y := range h {
		rows <- y
	}
	close(rows)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for y := range rows {
				f(y)
			}
		})
	}
	wg.Wait()
}
