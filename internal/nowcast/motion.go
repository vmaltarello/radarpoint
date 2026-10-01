package nowcast

import (
	"cmp"
	"math"
	"runtime"
	"slices"
	"sync"
)

// Options tune the motion estimation. Sizes are in pixels of the original
// grid unless stated otherwise; Radar-DPC SRI pixels are 1 km.
type Options struct {
	// Factor downsamples the grids before matching, for speed and to smooth
	// out small-scale noise.
	Factor int
	// Block is the side of the matching blocks.
	Block int
	// MaxShift is the largest displacement searched per time step. With
	// 1 km pixels and 5-minute steps, 12 px is 144 km/h.
	MaxShift int
	// RainThreshold is the rain rate (mm/h) above which a pixel counts as
	// rain, and MinRainFraction the share of rain pixels a block needs for
	// its motion to be measured.
	RainThreshold   float32
	MinRainFraction float64
	// Cap limits rain rates during matching, so a few intense cores do not
	// dominate the comparison.
	Cap float32

	// Robust combines the motion of the frame pairs with a weighted median
	// instead of a weighted mean, so one bad match does not drag a block.
	Robust bool
	// RecencyDecay weighs frame pairs by age: the newest has weight 1, the
	// one before RecencyDecay, then RecencyDecay², and so on. 1 weighs all
	// pairs equally.
	RecencyDecay float64

	// SmoothPerStep is the standard deviation, in pixels, of a Gaussian
	// smoothing applied to the forecast, growing by this much at each step:
	// small cells are not predictable for long, so the forecast blurs them
	// as lead time grows. 0 disables it.
	SmoothPerStep float64

	// The probability of rain (≥ ProbThreshold mm/h) is the share of rain
	// pixels in a square of half side ProbRadius + ProbRadiusPerStep × step
	// pixels around the place the rain comes from.
	ProbThreshold     float32
	ProbRadius        float64
	ProbRadiusPerStep float64
}

// DefaultOptions suit Radar-DPC SRI: 2 km matching cells, 48 km blocks, up
// to 144 km/h. Smoothing and probability radius were chosen with
// cmd/nowcastverify over 20 cases (see README): robust combination and
// recency weights made no measurable difference and are off.
var DefaultOptions = Options{
	Factor:            2,
	Block:             48,
	MaxShift:          12,
	RainThreshold:     0.2,
	MinRainFraction:   0.05,
	Cap:               30,
	RecencyDecay:      1,
	SmoothPerStep:     0.75,
	ProbThreshold:     0.2,
	ProbRadius:        2,
	ProbRadiusPerStep: 2,
}

// Motion is a field of displacement vectors, one per block, in pixels of the
// original grid per time step. U grows eastwards (columns), V southwards
// (rows).
type Motion struct {
	Block    int
	NX, NY   int
	U, V     []float64
	Measured int // blocks whose motion was measured rather than filled in
}

// Pair is two frames and the number of time steps between them.
type Pair struct {
	Prev, Next *Field
	Steps      float64
}

// PairMotion is the motion measured between the two frames of a pair, per
// block, in pixels of the original grid per time step.
type PairMotion struct {
	NX, NY int
	U, V   []float64
	OK     []bool // the block had enough rain to be measured
}

// MeasurePair runs the block matching on one pair of frames.
func MeasurePair(p Pair, o Options) *PairMotion {
	w, h := p.Next.W, p.Next.H
	pm := &PairMotion{NX: (w + o.Block - 1) / o.Block, NY: (h + o.Block - 1) / o.Block}
	n := pm.NX * pm.NY
	pm.U, pm.V, pm.OK = make([]float64, n), make([]float64, n), make([]bool, n)
	prev, next := p.Prev.downsample(o.Factor), p.Next.downsample(o.Factor)
	cb := o.Block / o.Factor
	f := float64(o.Factor) / p.Steps
	// Blocks are independent: spread the rows of blocks over the CPUs.
	// Each block index is written by exactly one goroutine.
	var wg sync.WaitGroup
	rows := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for by := range rows {
				for bx := 0; bx < pm.NX; bx++ {
					dx, dy, ok := matchBlock(prev, next, bx*cb, by*cb, cb, o.MaxShift/o.Factor, o)
					if ok {
						i := by*pm.NX + bx
						pm.U[i], pm.V[i], pm.OK[i] = dx*f, dy*f, true
					}
				}
			}
		})
	}
	for by := 0; by < pm.NY; by++ {
		rows <- by
	}
	close(rows)
	wg.Wait()
	return pm
}

// EstimateMotion measures the motion between each pair (oldest first) and
// combines the results per block.
func EstimateMotion(pairs []Pair, o Options) *Motion {
	pms := make([]*PairMotion, len(pairs))
	for i, p := range pairs {
		pms[i] = MeasurePair(p, o)
	}
	return CombineMotion(pms, o)
}

// CombineMotion merges per-pair motions (oldest first) into one field: per
// block, a weighted mean or median (Options.Robust) of the measured
// vectors, newer pairs weighing more (Options.RecencyDecay). Blocks never
// measured get the median of the others; the field is then smoothed. With
// no measurable block at all the motion is zero and Measured is 0.
func CombineMotion(pms []*PairMotion, o Options) *Motion {
	if len(pms) == 0 {
		return nil
	}
	m := &Motion{Block: o.Block, NX: pms[0].NX, NY: pms[0].NY}
	n := m.NX * m.NY
	weights := make([]float64, len(pms))
	decay := o.RecencyDecay
	if decay <= 0 {
		decay = 1
	}
	for i := range pms {
		weights[i] = math.Pow(decay, float64(len(pms)-1-i))
	}

	m.U, m.V = make([]float64, n), make([]float64, n)
	measured := make([]bool, n)
	var us, vs, ws []float64
	var allU, allV []float64
	for b := range n {
		us, vs, ws = us[:0], vs[:0], ws[:0]
		for i, pm := range pms {
			if pm.OK[b] {
				us, vs, ws = append(us, pm.U[b]), append(vs, pm.V[b]), append(ws, weights[i])
			}
		}
		if len(us) == 0 {
			continue
		}
		if o.Robust {
			m.U[b], m.V[b] = weightedMedian(us, ws), weightedMedian(vs, ws)
		} else {
			m.U[b], m.V[b] = weightedMean(us, ws), weightedMean(vs, ws)
		}
		measured[b] = true
		allU, allV = append(allU, m.U[b]), append(allV, m.V[b])
	}
	m.Measured = len(allU)
	if m.Measured == 0 {
		return m
	}
	mu, mv := median(allU), median(allV)
	for b := range n {
		if !measured[b] {
			m.U[b], m.V[b] = mu, mv
		}
	}
	m.smooth()
	return m
}

// matchBlock finds the displacement (dx, dy) of the block whose top-left
// corner is (x0, y0) such that next(p) ≈ prev(p − d), with sub-pixel
// refinement. ok is false if the block has too little rain or overlap.
func matchBlock(prev, next *Field, x0, y0, size, maxShift int, o Options) (dx, dy float64, ok bool) {
	x1, y1 := min(x0+size, next.W), min(y0+size, next.H)
	pixels := (x1 - x0) * (y1 - y0)
	rain := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if v := next.V[y*next.W+x]; v >= o.RainThreshold {
				rain++
			}
		}
	}
	if float64(rain) < o.MinRainFraction*float64(pixels) {
		return 0, 0, false
	}

	side := 2*maxShift + 1
	scores := make([]float64, side*side)
	best, bi := math.Inf(1), -1
	for sy := -maxShift; sy <= maxShift; sy++ {
		for sx := -maxShift; sx <= maxShift; sx++ {
			var sum float64
			n := 0
			for y := y0; y < y1; y++ {
				py := y - sy
				if py < 0 || py >= prev.H {
					continue
				}
				for x := x0; x < x1; x++ {
					px := x - sx
					if px < 0 || px >= prev.W {
						continue
					}
					a, b := next.V[y*next.W+x], prev.V[py*prev.W+px]
					if a != a || b != b { // NaN
						continue
					}
					d := float64(min(a, o.Cap) - min(b, o.Cap))
					sum += d * d
					n++
				}
			}
			i := (sy+maxShift)*side + sx + maxShift
			if n < pixels/2 {
				scores[i] = math.Inf(1)
				continue
			}
			scores[i] = sum / float64(n)
			// Prefer the smaller shift on ties, so featureless blocks stay put.
			if scores[i] < best || (scores[i] == best && sx*sx+sy*sy < shift2(bi, side, maxShift)) {
				best, bi = scores[i], i
			}
		}
	}
	if bi < 0 {
		return 0, 0, false
	}
	ix, iy := bi%side, bi/side
	dx = float64(ix-maxShift) + refine(scores, ix, iy, side, 1, 0)
	dy = float64(iy-maxShift) + refine(scores, ix, iy, side, 0, 1)
	return dx, dy, true
}

func shift2(i, side, maxShift int) int {
	if i < 0 {
		return math.MaxInt
	}
	x, y := i%side-maxShift, i/side-maxShift
	return x*x + y*y
}

// refine fits a parabola through the minimum and its two neighbours along
// (ux, uy) and returns the offset of its vertex, within ±0.5.
func refine(s []float64, ix, iy, side, ux, uy int) float64 {
	ax, ay, bx, by := ix-ux, iy-uy, ix+ux, iy+uy
	if ax < 0 || ay < 0 || bx >= side || by >= side {
		return 0
	}
	a, c, b := s[ay*side+ax], s[iy*side+ix], s[by*side+bx]
	den := a - 2*c + b
	if math.IsInf(a, 0) || math.IsInf(b, 0) || den <= 0 {
		return 0
	}
	return math.Max(-0.5, math.Min(0.5, (a-b)/(2*den)))
}

// smooth replaces each vector with the mean of its 3×3 neighbourhood.
func (m *Motion) smooth() {
	u, v := slices.Clone(m.U), slices.Clone(m.V)
	for by := 0; by < m.NY; by++ {
		for bx := 0; bx < m.NX; bx++ {
			var su, sv float64
			n := 0
			for y := max(by-1, 0); y <= min(by+1, m.NY-1); y++ {
				for x := max(bx-1, 0); x <= min(bx+1, m.NX-1); x++ {
					su += u[y*m.NX+x]
					sv += v[y*m.NX+x]
					n++
				}
			}
			m.U[by*m.NX+bx], m.V[by*m.NX+bx] = su/float64(n), sv/float64(n)
		}
	}
}

// At interpolates the motion bilinearly between block centres at pixel
// coordinates (x, y), clamping at the edges.
func (m *Motion) At(x, y float64) (u, v float64) {
	fx := x/float64(m.Block) - 0.5
	fy := y/float64(m.Block) - 0.5
	fx = math.Max(0, math.Min(fx, float64(m.NX-1)))
	fy = math.Max(0, math.Min(fy, float64(m.NY-1)))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, m.NX-1), min(y0+1, m.NY-1)
	tx, ty := fx-float64(x0), fy-float64(y0)
	lerp := func(f []float64) float64 {
		top := f[y0*m.NX+x0]*(1-tx) + f[y0*m.NX+x1]*tx
		bot := f[y1*m.NX+x0]*(1-tx) + f[y1*m.NX+x1]*tx
		return top*(1-ty) + bot*ty
	}
	return lerp(m.U), lerp(m.V)
}

func weightedMean(x, w []float64) float64 {
	var s, sw float64
	for i := range x {
		s += x[i] * w[i]
		sw += w[i]
	}
	return s / sw
}

// weightedMedian returns the value where the cumulative weight of the
// sorted values reaches half of the total.
func weightedMedian(x, w []float64) float64 {
	idx := make([]int, len(x))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int { return cmp.Compare(x[a], x[b]) })
	var total float64
	for _, v := range w {
		total += v
	}
	var acc float64
	for _, i := range idx {
		acc += w[i]
		if acc >= total/2 {
			return x[i]
		}
	}
	return x[idx[len(idx)-1]]
}

func median(s []float64) float64 {
	s = slices.Clone(s)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
