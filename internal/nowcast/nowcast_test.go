package nowcast

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/geo"
)

// Synthetic grid: 1 km pixels in the Radar-DPC projection.
var (
	tm   = &geo.TransverseMercator{Lat0: 42, Lon0: 12.5, K0: 1, Ellipsoid: geo.WGS84}
	gt   = geo.GeoTransform{OriginX: -200000, OriginY: 200000, PixelW: 1000, PixelH: 1000}
	step = 5 * time.Minute
	base = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
)

type blob struct{ x, y, r, peak float64 }

// rainField draws Gaussian rain cells shifted by (sx, sy) pixels, with a
// band of nodata on the left edge as outside radar coverage.
func rainField(w, h int, blobs []blob, sx, sy float64) *Field {
	f := &Field{W: w, H: h, V: make([]float32, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < 10 {
				f.V[y*w+x] = float32(math.NaN())
				continue
			}
			var v float64
			for _, b := range blobs {
				dx, dy := float64(x)+0.5-(b.x+sx), float64(y)+0.5-(b.y+sy)
				v += b.peak * math.Exp(-(dx*dx+dy*dy)/(2*b.r*b.r))
			}
			if v < 0.05 {
				v = 0
			}
			f.V[y*w+x] = float32(v)
		}
	}
	return f
}

func randomBlobs(n, w, h int) []blob {
	r := rand.New(rand.NewPCG(1, 2))
	out := make([]blob, n)
	for i := range out {
		out[i] = blob{x: 20 + r.Float64()*float64(w-40), y: 20 + r.Float64()*float64(h-40),
			r: 4 + r.Float64()*8, peak: 1 + r.Float64()*20}
	}
	return out
}

// frames moves the blobs by (u, v) pixels per step for n steps.
func frames(blobs []blob, w, h, n int, u, v float64) []Frame {
	var out []Frame
	for i := range n {
		out = append(out, Frame{Time: base.Add(time.Duration(i) * step), Field: rainField(w, h, blobs, u*float64(i), v*float64(i))})
	}
	return out
}

func TestMotionUniform(t *testing.T) {
	const w, h = 400, 400
	for _, c := range []struct{ u, v float64 }{{3, -2}, {-5, 4}, {0, 0}, {7.5, 1.5}} {
		fr := frames(randomBlobs(60, w, h), w, h, 4, c.u, c.v)
		n, err := Compute(fr, tm, gt, step, 12, DefaultOptions)
		if err != nil {
			t.Fatal(err)
		}
		if n.Pairs != 3 || n.Motion.Measured == 0 {
			t.Fatalf("pairs %d, measured %d", n.Pairs, n.Motion.Measured)
		}
		for _, p := range [][2]float64{{100, 100}, {200, 300}, {350, 50}} {
			u, v := n.Motion.At(p[0], p[1])
			if math.Abs(u-c.u) > 0.75 || math.Abs(v-c.v) > 0.75 {
				t.Errorf("motion (%g,%g) at %v = (%.2f, %.2f)", c.u, c.v, p, u, v)
			}
		}
	}
}

func TestForecastFollowsTheRain(t *testing.T) {
	const w, h = 400, 400
	const u, v = 4.0, 0.0 // 4 km east every 5 minutes = 48 km/h
	// Background cells give the motion; one strong cell is tracked.
	blobs := append(randomBlobs(40, w, h), blob{x: 150, y: 200, r: 6, peak: 40})
	fr := frames(blobs, w, h, 4, u, v)
	n, err := Compute(fr, tm, gt, step, 12, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	// After 3 steps the cell centre is at x=162. In 6 more steps (30 min)
	// it should reach x=186; pick the target pixel there.
	target := gt
	lat, lon := tm.Inverse(target.Center(186, 199))
	pts, err := n.Forecast(lat, lon)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 13 || pts[0].Lead != 0 || pts[12].Lead != time.Hour || !pts[0].Time.Equal(base.Add(15*time.Minute)) {
		t.Fatalf("points %+v", pts)
	}
	// The strongest forecast value must come at +30 minutes (±5).
	best := 0
	for i, p := range pts {
		if p.Value > pts[best].Value {
			best = i
		}
	}
	if best < 5 || best > 7 || pts[best].Value < 20 {
		t.Errorf("peak %.1f at +%v, want ≥20 at +30m", pts[best].Value, pts[best].Lead)
	}

	speed, toward, ok := n.Velocity(lat, lon)
	if !ok || math.Abs(speed-48) > 6 || math.Abs(toward-90) > 10 {
		t.Errorf("velocity %.1f km/h toward %.0f°, want 48 toward 90 (east)", speed, toward)
	}
}

func TestForecastFromOutsideCoverage(t *testing.T) {
	const w, h = 400, 400
	// Rain moves east; a point just right of the nodata band looks back
	// into it after a few steps.
	fr := frames(randomBlobs(60, w, h), w, h, 3, 5, 0)
	n, err := Compute(fr, tm, gt, step, 12, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	lat, lon := tm.Inverse(gt.Center(14, 200))
	pts, _ := n.Forecast(lat, lon)
	if !math.IsNaN(pts[12].Value) {
		t.Errorf("+60m value %v, want NaN (comes from outside coverage)", pts[12].Value)
	}
}

func TestNoRain(t *testing.T) {
	const w, h = 200, 200
	fr := frames(nil, w, h, 3, 0, 0)
	n, err := Compute(fr, tm, gt, step, 12, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if n.Motion.Measured != 0 {
		t.Fatalf("measured %d blocks without rain", n.Motion.Measured)
	}
	lat, lon := tm.Inverse(gt.Center(100, 100))
	pts, _ := n.Forecast(lat, lon)
	for _, p := range pts {
		if p.Value != 0 {
			t.Fatalf("forecast %v without rain", p)
		}
	}
	if _, _, ok := n.Velocity(lat, lon); !ok {
		t.Error("velocity should be available (zero)")
	}
}

func TestComputeErrors(t *testing.T) {
	f := rainField(50, 50, nil, 0, 0)
	if _, err := Compute([]Frame{{base, f}}, tm, gt, step, 12, DefaultOptions); err != ErrNotEnoughFrames {
		t.Errorf("one frame: %v", err)
	}
	// Two frames an hour apart are too far for a 5-minute step.
	far := []Frame{{base, f}, {base.Add(time.Hour), f}}
	if _, err := Compute(far, tm, gt, step, 12, DefaultOptions); err != ErrNotEnoughFrames {
		t.Errorf("distant frames: %v", err)
	}
}

func TestOutsidePoint(t *testing.T) {
	fr := frames(nil, 50, 50, 2, 0, 0)
	n, _ := Compute(fr, tm, gt, step, 12, DefaultOptions)
	if _, err := n.Forecast(48, 5); err == nil {
		t.Error("expected ErrOutside")
	}
}

func TestForecastFieldsMatchPointForecast(t *testing.T) {
	const w, h = 200, 200
	fr := frames(randomBlobs(30, w, h), w, h, 3, 3, 2)
	n, err := Compute(fr, tm, gt, step, 6, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	fields := n.ForecastFields(n.Latest, 6)
	for _, p := range [][2]int{{50, 50}, {120, 80}, {180, 190}} {
		lat, lon := tm.Inverse(gt.Center(p[0], p[1]))
		pts, err := n.Forecast(lat, lon)
		if err != nil {
			t.Fatal(err)
		}
		for k := 1; k <= 6; k++ {
			a, b := float64(fields[k-1].At(p[0], p[1])), pts[k].Value
			if a != b && !(math.IsNaN(a) && math.IsNaN(b)) {
				t.Errorf("pixel %v step %d: field %v, point %v", p, k, a, b)
			}
		}
	}
}
