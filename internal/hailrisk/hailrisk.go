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

// The signals, each the maximum over the next Steps steps along the
// motion: POH, VIL, VIL growth, ETM and ETM growth.
const (
	SigPOH = iota
	SigVIL
	SigVILGrowth
	SigETM
	SigETMGrowth
	NumSignals
)

// Signals are the model inputs at one pixel: each signal's maximum along the
// pixel's trajectory (At), the same maximum anywhere within Radius (Near),
// and VIL and POH now at the pixel. NaN means no data.
type Signals struct {
	At, Near       [NumSignals]float32
	VILNow, POHNow float32
}

// Prob returns the probability of hail within 30 minutes: the calibrated
// model, and at least the current POH where it already shows hail.
func (s Signals) Prob() float64 {
	a := func(j int) float64 { return nz(s.At[j]) }
	b := func(j int) float64 { return nz(s.Near[j]) }
	x := [len(beta)]float64{1, a(SigPOH), b(SigPOH),
		min(a(SigVIL), 40) / 10, min(b(SigVIL), 40) / 10,
		clip(a(SigVILGrowth), -20, 20) / 10, clip(b(SigVILGrowth), -20, 40) / 10,
		a(SigETM) / 10000, b(SigETM) / 10000,
		clip(a(SigETMGrowth), -5000, 8000) / 5000, clip(b(SigETMGrowth), -5000, 10000) / 5000,
		min(nz(s.VILNow), 40) / 10, nz(s.POHNow)}
	z := 0.0
	for j, c := range beta {
		z += c * x[j]
	}
	p := calibrate(1 / (1 + math.Exp(-clip(z, -30, 30))))
	if poh := nz(s.POHNow); poh >= Hail {
		p = max(p, poh)
	}
	return p
}

// along follows the trajectory of pixel (c, r) back over Steps steps and
// returns the maximum of each signal met on the way.
func along(n *nowcast.Nowcast, in Inputs, c, r int) [NumSignals]float32 {
	growth := func(now, old *nowcast.Field, c, r int) float32 {
		x, y := float64(c)+0.5, float64(r)+0.5
		for range GrowthSteps {
			x, y = n.StepBack(x, y)
		}
		return now.At(c, r) - old.At(int(math.Floor(x)), int(math.Floor(y)))
	}
	mx := [NumSignals]float32{nan, nan, nan, nan, nan}
	x, y := float64(c)+0.5, float64(r)+0.5
	for range Steps {
		x, y = n.StepBack(x, y)
		qc, qr := int(math.Floor(x)), int(math.Floor(y))
		up(&mx[SigPOH], in.POH.At(qc, qr))
		up(&mx[SigVIL], in.VIL.At(qc, qr))
		up(&mx[SigVILGrowth], growth(in.VIL, in.VILOld, qc, qr))
		up(&mx[SigETM], in.ETM.At(qc, qr))
		up(&mx[SigETMGrowth], growth(in.ETM, in.ETMOld, qc, qr))
	}
	return mx
}

// SignalField holds the signals of every pixel of the grid.
type SignalField struct {
	W, H     int
	At, Near [NumSignals][]float32
	in       Inputs
}

// NewSignalField computes the signals of every pixel, spread over all CPUs.
func NewSignalField(n *nowcast.Nowcast, in Inputs) *SignalField {
	w, h := in.POH.W, in.POH.H
	f := &SignalField{W: w, H: h, in: in}
	for j := range f.At {
		f.At[j] = make([]float32, w*h)
	}
	rows(h, func(r int) {
		for c := range w {
			mx := along(n, in, c, r)
			for j := range mx {
				f.At[j][r*w+c] = mx[j]
			}
		}
	})
	for j := range f.Near {
		f.Near[j] = maxFilter(f.At[j], w, h, Radius)
	}
	return f
}

// Pixel returns the signals of pixel i (row-major).
func (f *SignalField) Pixel(i int) Signals {
	s := Signals{VILNow: f.in.VIL.V[i], POHNow: f.in.POH.V[i]}
	for j := range s.At {
		s.At[j], s.Near[j] = f.At[j][i], f.Near[j][i]
	}
	return s
}

// SignalsAt computes the signals of pixel (c, r) alone, following only the
// trajectories within Radius of it: much faster than NewSignalField when
// few pixels are needed, with the same result.
func SignalsAt(n *nowcast.Nowcast, in Inputs, c, r int) Signals {
	w, h := in.POH.W, in.POH.H
	s := Signals{VILNow: in.VIL.At(c, r), POHNow: in.POH.At(c, r)}
	for j := range s.Near {
		s.At[j], s.Near[j] = nan, nan
	}
	for qr := max(r-Radius, 0); qr <= min(r+Radius, h-1); qr++ {
		for qc := max(c-Radius, 0); qc <= min(c+Radius, w-1); qc++ {
			mx := along(n, in, qc, qr)
			for j := range mx {
				up(&s.Near[j], mx[j])
			}
			if qc == c && qr == r {
				s.At = mx
			}
		}
	}
	return s
}

// Compute returns the probability of hail at every pixel within the next
// 30 minutes (see Signals.Prob). Pixels without POH data are NaN.
func Compute(n *nowcast.Nowcast, in Inputs) *nowcast.Field {
	f := NewSignalField(n, in)
	out := &nowcast.Field{W: f.W, H: f.H, V: make([]float32, f.W*f.H)}
	rows(f.H, func(r int) {
		for c := range f.W {
			i := r*f.W + c
			if p := in.POH.V[i]; p != p {
				out.V[i] = nan
				continue
			}
			out.V[i] = float32(f.Pixel(i).Prob())
		}
	})
	return out
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
