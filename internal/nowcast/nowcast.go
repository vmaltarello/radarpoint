package nowcast

import (
	"errors"
	"math"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/geo"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

// ErrNotEnoughFrames means fewer than two usable frames were available.
var ErrNotEnoughFrames = errors.New("nowcast: at least two frames are needed")

// Frame is one observation used as input.
type Frame struct {
	Time  time.Time
	Field *Field
}

// Nowcast is the motion field estimated at Base, ready to answer point
// queries. It is immutable once built and safe for concurrent use.
type Nowcast struct {
	Base   time.Time     // time of the latest observation
	Step   time.Duration // time step of the forecast
	Steps  int           // number of forecast steps
	Latest *Field
	Motion *Motion
	Pairs  int // frame pairs used to estimate the motion
	// Observed are the input frames, oldest first; the last one is Latest.
	Observed []Frame
	// Hail, if not nil, is the probability of hail observed at Base. It is
	// moved with the rain, since hail falls from the same storm cells.
	Hail *Field

	options   Options
	proj      geo.Projection
	transform geo.GeoTransform
	pixelKm   float64
}

// Compute estimates the motion from frames (any order) on a grid with the
// given projection and pixel transform. Pairs further apart than 3 steps are
// ignored as too unreliable.
func Compute(frames []Frame, proj geo.Projection, gt geo.GeoTransform, step time.Duration, steps int, o Options) (*Nowcast, error) {
	return compute(frames, proj, gt, step, steps, o, nil)
}

// pairKey identifies a frame pair by the times of its two frames.
type pairKey struct{ prev, next time.Time }

// compute is Compute with an optional cache of per-pair motions: the motion
// between two frames never changes, so only new pairs are measured.
func compute(frames []Frame, proj geo.Projection, gt geo.GeoTransform, step time.Duration, steps int, o Options, cache map[pairKey]*PairMotion) (*Nowcast, error) {
	if len(frames) < 2 {
		return nil, ErrNotEnoughFrames
	}
	sorted := slices.SortedFunc(slices.Values(frames), func(a, b Frame) int { return a.Time.Compare(b.Time) })
	var pms []*PairMotion
	for i := 1; i < len(sorted); i++ {
		gap := sorted[i].Time.Sub(sorted[i-1].Time)
		if gap <= 0 || gap > 3*step {
			continue
		}
		key := pairKey{sorted[i-1].Time, sorted[i].Time}
		pm := cache[key]
		if pm == nil {
			pm = MeasurePair(Pair{Prev: sorted[i-1].Field, Next: sorted[i].Field, Steps: float64(gap) / float64(step)}, o)
			if cache != nil {
				cache[key] = pm
			}
		}
		pms = append(pms, pm)
	}
	if len(pms) == 0 {
		return nil, ErrNotEnoughFrames
	}
	latest := sorted[len(sorted)-1]
	n := &Nowcast{
		Base:      latest.Time,
		Step:      step,
		Steps:     steps,
		Latest:    latest.Field,
		Observed:  sorted,
		Motion:    CombineMotion(pms, o),
		Pairs:     len(pms),
		options:   o,
		proj:      proj,
		transform: gt,
	}
	if _, ok := proj.(*geo.TransverseMercator); ok {
		n.pixelKm = gt.PixelW / 1000
	}
	return n, nil
}

// Point is the forecast at one lead time. Value and Hail are NaN where the
// rain would come from outside the radar coverage; Hail is also NaN when the
// nowcast has no hail field. Hail is moved without the lead-time smoothing
// of the rain, which would wipe out its small cells (see HailFields).
type Point struct {
	Time  time.Time
	Lead  time.Duration
	Value float64
	Hail  float64
	// Probability of rain (0–1) near the point; NaN where too little of the
	// neighbourhood has radar data.
	Probability float64
}

// Forecast returns the observation (lead 0) and the forecast for every step
// at lat/lon, following the motion field backwards from the point.
// It returns raster.ErrOutside if the point is not on the grid.
func (n *Nowcast) Forecast(lat, lon float64) ([]Point, error) {
	mx, my := n.proj.Forward(lat, lon)
	col, row := n.transform.Pixel(mx, my)
	if col < 0 || row < 0 || col >= n.Latest.W || row >= n.Latest.H {
		return nil, raster.ErrOutside
	}
	x, y := float64(col)+0.5, float64(row)+0.5
	out := make([]Point, 0, n.Steps+1)
	for k := 0; k <= n.Steps; k++ {
		if k > 0 {
			x, y = n.stepBack(x, y)
		}
		px, py := int(math.Floor(x)), int(math.Floor(y))
		hail := math.NaN()
		if n.Hail != nil {
			hail = float64(n.Hail.At(px, py))
		}
		d := time.Duration(k) * n.Step
		out = append(out, Point{Time: n.Base.Add(d), Lead: d, Value: float64(n.valueAt(n.Latest, px, py, k)), Hail: hail,
			Probability: n.probAt(px, py, n.probRadius(k))})
	}
	return out, nil
}

// Velocity returns the speed (km/h) and the direction the rain moves
// towards (degrees clockwise from grid north) at lat/lon. ok is false when
// the grid is not in metres or the point is outside it. Grid north differs
// from true north by a few degrees far from the central meridian.
func (n *Nowcast) Velocity(lat, lon float64) (speedKmh, towardDeg float64, ok bool) {
	if n.pixelKm == 0 {
		return 0, 0, false
	}
	mx, my := n.proj.Forward(lat, lon)
	col, row := n.transform.Pixel(mx, my)
	if col < 0 || row < 0 || col >= n.Latest.W || row >= n.Latest.H {
		return 0, 0, false
	}
	u, v := n.Motion.At(float64(col)+0.5, float64(row)+0.5)
	east, north := u*n.pixelKm, -v*n.pixelKm // km per step
	speedKmh = math.Hypot(east, north) / n.Step.Hours()
	towardDeg = math.Mod(math.Atan2(east, north)*180/math.Pi+360, 360)
	return speedKmh, towardDeg, true
}

// ForecastFields moves src (the latest rain, or any field observed at Base)
// along the motion, smoothed as the lead time grows, and returns the whole
// grid at steps 1…steps, with NaN where the values would come from outside
// the radar coverage. It is meant for verification and maps; point queries
// should use Forecast. Rows are spread over all CPUs.
func (n *Nowcast) ForecastFields(src *Field, steps int) []*Field {
	return n.move(src, steps, true)
}

// HailFields moves the hail field like ForecastFields, but without the
// lead-time smoothing: hail cells are a few kilometres wide, and blurring
// them as much as the rain lowers every value under 30–50% within half an
// hour. On 150 hail moments of summer 2026, moving it unsmoothed scored
// better at every lead and scale. It returns nil without a hail field.
func (n *Nowcast) HailFields(steps int) []*Field {
	if n.Hail == nil {
		return nil
	}
	return n.move(n.Hail, steps, false)
}

func (n *Nowcast) move(src *Field, steps int, smooth bool) []*Field {
	w, h := src.W, src.H
	out := make([]*Field, steps)
	for k := range out {
		out[k] = &Field{W: w, H: h, V: make([]float32, w*h)}
	}
	// With lead-time smoothing, sample a blurred copy per step: the same
	// values as valueAt, computed for the whole grid at once.
	srcs := make([]*Field, steps)
	for k := range srcs {
		srcs[k] = src
		if s := n.sigma(k + 1); s > 0 && smooth {
			srcs[k] = blur(src, s)
		}
	}
	var wg sync.WaitGroup
	rows := make(chan int, h)
	for row := range h {
		rows <- row
	}
	close(rows)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for row := range rows {
				for col := 0; col < w; col++ {
					x, y := float64(col)+0.5, float64(row)+0.5
					for k := range steps {
						x, y = n.stepBack(x, y)
						out[k].V[row*w+col] = srcs[k].At(int(math.Floor(x)), int(math.Floor(y)))
					}
				}
			}
		})
	}
	wg.Wait()
	return out
}

// WithHail returns a copy of the nowcast that also moves the given hail
// field, observed at the same time as Base.
func (n *Nowcast) WithHail(hail *Field) *Nowcast {
	c := *n
	c.Hail = hail
	return &c
}

// StepBack moves pixel position (x, y), in pixel units with (0.5, 0.5) at
// the centre of the top-left pixel, one time step back along the motion:
// where what is at (x, y) one step later comes from.
func (n *Nowcast) StepBack(x, y float64) (float64, float64) { return n.stepBack(x, y) }

// stepBack moves pixel position (x, y) one time step back along the motion,
// using the motion at the midpoint so curved flows are followed.
func (n *Nowcast) stepBack(x, y float64) (float64, float64) {
	u, v := n.Motion.At(x, y)
	u, v = n.Motion.At(x-u/2, y-v/2)
	return x - u, y - v
}

// Projection and Transform describe the grid the nowcast is computed on.
func (n *Nowcast) Projection() geo.Projection  { return n.proj }
func (n *Nowcast) Transform() geo.GeoTransform { return n.transform }

// sigma is the smoothing at step k, in pixels; 0 means none.
func (n *Nowcast) sigma(k int) float64 { return n.options.SmoothPerStep * float64(k) }

// valueAt reads f at pixel (x, y) for step k, smoothed as the lead time
// requires.
func (n *Nowcast) valueAt(f *Field, x, y, k int) float32 {
	if s := n.sigma(k); s > 0 {
		return smoothAt(f, x, y, s)
	}
	return f.At(x, y)
}

// PixelOf returns the grid pixel at lat/lon, and false outside the grid.
func (n *Nowcast) PixelOf(lat, lon float64) (col, row int, ok bool) {
	mx, my := n.proj.Forward(lat, lon)
	col, row = n.transform.Pixel(mx, my)
	return col, row, col >= 0 && row >= 0 && col < n.Latest.W && row < n.Latest.H
}
