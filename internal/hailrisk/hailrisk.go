// Package hailrisk estimates, for every pixel, the probability of hail
// within the next 30 minutes from the latest radar products.
//
// The radar's probability of hail (POH) is moved along the rain motion, like
// the rain nowcast. Two more products tell growing storms: VIL (vertically
// integrated liquid water) and ETM (maximum echo top height), with their
// growth over the last 10 minutes. A logistic model combines them; it was
// fitted on the 150 moments with the most hail of summer 2026 and checked on
// the whole season (see README).
package hailrisk

import (
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

const (
	// Steps is the number of 5-minute steps the probability covers.
	Steps = 6
	// Radius is the half side, in pixels (km), of the square around a pixel
	// where nearby hail and storms count.
	Radius = 5
	// GrowthSteps is how far back the growth of VIL and ETM is measured.
	GrowthSteps = 2
)

// Hail is the POH value from which hail is counted: 50%.
const Hail = 0.5

// beta are the model coefficients for the features of features().
var beta = [...]float64{-7.020609991486959, 1.5424296703122726, 2.18389913533486, -0.13027298904166415,
	-0.11382387394438717, 0.618336016655884, 0.4083217259053841, -0.4000135398122394, 1.6648363718212726,
	0.4740374310629384, 0.45107200946680537, -0.032006103421526676, 0.7731615608893179}

// Inputs are the products at the nowcast base time, and VIL and ETM
// GrowthSteps earlier. All fields are on the nowcast grid.
type Inputs struct {
	POH, VIL, ETM  *nowcast.Field
	VILOld, ETMOld *nowcast.Field
}

// NewField decodes VIL or ETM: -9999 (outside radar coverage) becomes NaN,
// and ETM's -9998 (coverage but no echo) becomes 0.
func NewField(g *raster.GeoTIFF) (*nowcast.Field, error) {
	f, err := nowcast.NewField(g, func(v float64) bool { return v == -9999 })
	if err != nil {
		return nil, err
	}
	noEchoToZero(f)
	return f, nil
}

func noEchoToZero(f *nowcast.Field) {
	for i, v := range f.V {
		if v == -9998 {
			f.V[i] = 0
		}
	}
}

// Risk is the probability of hail (0–1) within Steps steps of Base.
type Risk struct {
	Base time.Time
	Prob *nowcast.Field
}

// Compute returns the probability of hail at every pixel within the next
// 30 minutes. Where POH already shows hail, the value is at least the
// current POH. Pixels without POH data are NaN.
func Compute(n *nowcast.Nowcast, in Inputs) *nowcast.Field {
	w, h := in.POH.W, in.POH.H
	// Maximum along the trajectory of each pixel over the next Steps steps
	// of POH, VIL, VIL growth, ETM and ETM growth.
	var along [5][]float32
	for j := range along {
		along[j] = make([]float32, w*h)
	}
	growth := func(now, old *nowcast.Field, c, r int) float32 {
		x, y := float64(c)+0.5, float64(r)+0.5
		for range GrowthSteps {
			x, y = n.StepBack(x, y)
		}
		return now.At(c, r) - old.At(int(math.Floor(x)), int(math.Floor(y)))
	}
	rows(h, func(r int) {
		for c := range w {
			mx := [5]float32{nan, nan, nan, nan, nan}
			x, y := float64(c)+0.5, float64(r)+0.5
			for range Steps {
				x, y = n.StepBack(x, y)
				qc, qr := int(math.Floor(x)), int(math.Floor(y))
				up(&mx[0], in.POH.At(qc, qr))
				up(&mx[1], in.VIL.At(qc, qr))
				up(&mx[2], growth(in.VIL, in.VILOld, qc, qr))
				up(&mx[3], in.ETM.At(qc, qr))
				up(&mx[4], growth(in.ETM, in.ETMOld, qc, qr))
			}
			for j := range mx {
				along[j][r*w+c] = mx[j]
			}
		}
	})
	var near [5][]float32
	for j := range near {
		near[j] = maxFilter(along[j], w, h, Radius)
	}
	out := &nowcast.Field{W: w, H: h, V: make([]float32, w*h)}
	rows(h, func(r int) {
		for c := range w {
			i := r*w + c
			poh := in.POH.V[i]
			if poh != poh {
				out.V[i] = nan
				continue
			}
			x := features(along, near, i, nz(in.VIL.V[i]), float64(poh))
			z := 0.0
			for j, b := range beta {
				z += b * x[j]
			}
			p := calibrate(1 / (1 + math.Exp(-max(-30, min(30, z)))))
			out.V[i] = float32(max(p, float64(poh)*boolf(poh >= Hail)))
		}
	})
	return out
}

// features are the model inputs at pixel i, scaled as when it was fitted.
func features(along, near [5][]float32, i int, vilNow, pohNow float64) [len(beta)]float64 {
	a := func(j int) float64 { return nz(along[j][i]) }
	b := func(j int) float64 { return nz(near[j][i]) }
	return [len(beta)]float64{1, a(0), b(0),
		min(a(1), 40) / 10, min(b(1), 40) / 10,
		clip(a(2), -20, 20) / 10, clip(b(2), -20, 40) / 10,
		a(3) / 10000, b(3) / 10000,
		clip(a(4), -5000, 8000) / 5000, clip(b(4), -5000, 10000) / 5000,
		min(vilNow, 40) / 10, pohNow}
}

// calibrate corrects the top of the model's range, which is overconfident:
// on the days not used to fit it, 70–100% verified at 67%. Values up to 50%
// are kept; above, the excess is scaled so 100% becomes 70%.
func calibrate(p float64) float64 {
	if p <= 0.5 {
		return p
	}
	return 0.5 + (p-0.5)*0.4
}

var nan = float32(math.NaN())

// up raises *m to v, ignoring NaN.
func up(m *float32, v float32) {
	if v == v && (*m != *m || v > *m) {
		*m = v
	}
}

func nz(v float32) float64 {
	if v != v {
		return 0
	}
	return float64(v)
}

func clip(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// maxFilter returns the maximum over a square of side 2r+1 around each
// pixel, ignoring NaN, in two separable passes.
func maxFilter(v []float32, w, h, r int) []float32 {
	tmp, out := make([]float32, len(v)), make([]float32, len(v))
	rows(h, func(y int) {
		for x := range w {
			m := nan
			for d := max(x-r, 0); d <= min(x+r, w-1); d++ {
				up(&m, v[y*w+d])
			}
			tmp[y*w+x] = m
		}
	})
	rows(h, func(y int) {
		for x := range w {
			m := nan
			for d := max(y-r, 0); d <= min(y+r, h-1); d++ {
				up(&m, tmp[d*w+x])
			}
			out[y*w+x] = m
		}
	})
	return out
}

// rows calls f for every row, spread over all CPUs.
func rows(h int, f func(r int)) {
	ch := make(chan int, h)
	for r := range h {
		ch <- r
	}
	close(ch)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for r := range ch {
				f(r)
			}
		})
	}
	wg.Wait()
}
