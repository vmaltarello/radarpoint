package nowcast

import (
	"errors"
	"math"
	"slices"
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

	proj      geo.Projection
	transform geo.GeoTransform
	pixelKm   float64
}

// Compute estimates the motion from frames (any order) on a grid with the
// given projection and pixel transform. Pairs further apart than 3 steps are
// ignored as too unreliable.
func Compute(frames []Frame, proj geo.Projection, gt geo.GeoTransform, step time.Duration, steps int, o Options) (*Nowcast, error) {
	if len(frames) < 2 {
		return nil, ErrNotEnoughFrames
	}
	sorted := slices.SortedFunc(slices.Values(frames), func(a, b Frame) int { return a.Time.Compare(b.Time) })
	var pairs []Pair
	for i := 1; i < len(sorted); i++ {
		gap := sorted[i].Time.Sub(sorted[i-1].Time)
		if gap <= 0 || gap > 3*step {
			continue
		}
		pairs = append(pairs, Pair{Prev: sorted[i-1].Field, Next: sorted[i].Field, Steps: float64(gap) / float64(step)})
	}
	if len(pairs) == 0 {
		return nil, ErrNotEnoughFrames
	}
	latest := sorted[len(sorted)-1]
	n := &Nowcast{
		Base:      latest.Time,
		Step:      step,
		Steps:     steps,
		Latest:    latest.Field,
		Motion:    EstimateMotion(pairs, o),
		Pairs:     len(pairs),
		proj:      proj,
		transform: gt,
	}
	if _, ok := proj.(*geo.TransverseMercator); ok {
		n.pixelKm = gt.PixelW / 1000
	}
	return n, nil
}

// Point is the forecast at one lead time. Value is NaN where the rain would
// come from outside the radar coverage.
type Point struct {
	Time  time.Time
	Lead  time.Duration
	Value float64
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
		val := float64(n.Latest.At(int(math.Floor(x)), int(math.Floor(y))))
		d := time.Duration(k) * n.Step
		out = append(out, Point{Time: n.Base.Add(d), Lead: d, Value: val})
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

// ForecastFields returns the forecast for the whole grid at steps 1…steps,
// with NaN where the rain would come from outside the radar coverage. It is
// meant for verification and maps; point queries should use Forecast.
func (n *Nowcast) ForecastFields(steps int) []*Field {
	w, h := n.Latest.W, n.Latest.H
	out := make([]*Field, steps)
	for k := range out {
		out[k] = &Field{W: w, H: h, V: make([]float32, w*h)}
	}
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			x, y := float64(col)+0.5, float64(row)+0.5
			for k := range steps {
				x, y = n.stepBack(x, y)
				out[k].V[row*w+col] = n.Latest.At(int(math.Floor(x)), int(math.Floor(y)))
			}
		}
	}
	return out
}

// stepBack moves pixel position (x, y) one time step back along the motion,
// using the motion at the midpoint so curved flows are followed.
func (n *Nowcast) stepBack(x, y float64) (float64, float64) {
	u, v := n.Motion.At(x, y)
	u, v = n.Motion.At(x-u/2, y-v/2)
	return x - u, y - v
}
