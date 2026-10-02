package hailrisk

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/geo"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

const w, h = 200, 200

var (
	tm   = &geo.TransverseMercator{Lat0: 42, Lon0: 12.5, K0: 1, Ellipsoid: geo.WGS84}
	gt   = geo.GeoTransform{OriginX: -100000, OriginY: 100000, PixelW: 1000, PixelH: 1000}
	step = 5 * time.Minute
	base = time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
)

// movingRain returns a nowcast whose rain moves 2 pixels east per step.
func movingRain(t *testing.T) *nowcast.Nowcast {
	r := rand.New(rand.NewPCG(3, 4))
	type blob struct{ x, y, r, peak float64 }
	var blobs []blob
	for range 40 {
		blobs = append(blobs, blob{20 + r.Float64()*160, 20 + r.Float64()*160, 4 + r.Float64()*6, 1 + r.Float64()*20})
	}
	var fr []nowcast.Frame
	for i := range 3 {
		f := &nowcast.Field{W: w, H: h, V: make([]float32, w*h)}
		for y := range h {
			for x := range w {
				var v float64
				for _, b := range blobs {
					dx, dy := float64(x)+0.5-(b.x+2*float64(i)), float64(y)+0.5-b.y
					v += b.peak * math.Exp(-(dx*dx+dy*dy)/(2*b.r*b.r))
				}
				f.V[y*w+x] = float32(v)
			}
		}
		fr = append(fr, nowcast.Frame{Time: base.Add(time.Duration(i-2) * step), Field: f})
	}
	n, err := nowcast.Compute(fr, tm, gt, step, Steps, nowcast.DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func constant(v float32) *nowcast.Field {
	f := &nowcast.Field{W: w, H: h, V: make([]float32, w*h)}
	for i := range f.V {
		f.V[i] = v
	}
	return f
}

// storm sets a 3×3 square around (x, y) to v.
func storm(f *nowcast.Field, x, y int, v float32) *nowcast.Field {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			f.V[(y+dy)*w+x+dx] = v
		}
	}
	return f
}

func TestComputeFollowsAGrowingHailCell(t *testing.T) {
	n := movingRain(t)
	// A growing hailstorm at (60, 100): VIL from 5 to 30 kg/m², tops from
	// 6 to 11 km in 10 minutes. It moves 2 pixels east every 5 minutes.
	in := Inputs{
		POH:    storm(constant(0), 60, 100, 0.9),
		VIL:    storm(constant(0), 60, 100, 30),
		ETM:    storm(constant(0), 60, 100, 11000),
		VILOld: storm(constant(0), 56, 100, 5),
		ETMOld: storm(constant(0), 56, 100, 6000),
	}
	in.POH.V[0] = float32(math.NaN()) // a pixel without radar data
	p := Compute(n, in)
	at := func(x, y int) float32 { return p.V[y*w+x] }

	if v := at(60, 100); v < 0.9 {
		t.Errorf("hail now at the cell: %v, want at least the POH 0.9", v)
	}
	if v := at(68, 100); v < 0.4 { // reached in 20 minutes
		t.Errorf("downstream in the cell's path: %v, want high", v)
	}
	if v := at(40, 100); v > 0.05 {
		t.Errorf("upstream, where the cell has been: %v, want low", v)
	}
	if v := at(150, 30); v > 0.01 {
		t.Errorf("far from any storm: %v, want about 0", v)
	}
	if v := at(0, 0); v == v {
		t.Errorf("pixel without POH: %v, want NaN", v)
	}
	for i, v := range p.V {
		if v == v && (v < 0 || v > 1) {
			t.Fatalf("pixel %d: probability %v outside 0–1", i, v)
		}
	}
}

func TestCalibrate(t *testing.T) {
	if calibrate(0.3) != 0.3 || calibrate(0.5) != 0.5 || math.Abs(calibrate(1)-0.7) > 1e-12 {
		t.Errorf("calibrate: 0.3→%v 0.5→%v 1→%v", calibrate(0.3), calibrate(0.5), calibrate(1))
	}
	for p := 0.0; p < 1; p += 0.01 {
		if calibrate(p+0.01) < calibrate(p) {
			t.Fatalf("calibrate decreases at %v", p)
		}
	}
}

func TestNoEchoIsZero(t *testing.T) {
	f := &nowcast.Field{W: 3, H: 1, V: []float32{-9998, 8300, float32(math.NaN())}}
	noEchoToZero(f)
	if f.V[0] != 0 || f.V[1] != 8300 || f.V[2] == f.V[2] {
		t.Errorf("got %v, want [0 8300 NaN]", f.V)
	}
}

func TestTrackerWaitsForAllInputs(t *testing.T) {
	n := movingRain(t)
	var tr Tracker
	// No inputs yet: nothing computed, no error.
	if ok, err := tr.Update(n, func(string, time.Time) *raster.GeoTIFF { return nil }); ok || err != nil || tr.Current() != nil {
		t.Fatalf("update without inputs: %v %v", ok, err)
	}
	if ok, err := tr.Update(nil, nil); ok || err != nil {
		t.Fatalf("update without nowcast: %v %v", ok, err)
	}
}

func TestTrackerComputesOncePerNowcast(t *testing.T) {
	g, err := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	e := &nowcast.Engine{Steps: Steps, Options: nowcast.DefaultOptions, IsNoData: func(v float64) bool { return v == -9999 }}
	n, err := e.Update([]*store.Frame{
		{Product: "SRI", Time: base.Add(-step), Period: step, Grid: g},
		{Product: "SRI", Time: base, Period: step, Grid: g},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same crop stands in for every product: only the plumbing is tested.
	var asked []string
	frame := func(p string, at time.Time) *raster.GeoTIFF {
		asked = append(asked, p+" "+at.Sub(base).String())
		return g
	}
	var tr Tracker
	if ok, err := tr.Update(n, frame); !ok || err != nil {
		t.Fatalf("first update: %v %v", ok, err)
	}
	if r := tr.Current(); r == nil || !r.Base.Equal(base) || r.Prob.W != g.Width || r.Prob.H != g.Height {
		t.Fatalf("risk %+v", tr.Current())
	}
	want := "[POH 0s VIL 0s ETM 0s VIL -10m0s ETM -10m0s]"
	if got := fmt.Sprint(asked); got != want {
		t.Errorf("inputs asked %s, want %s", got, want)
	}
	if ok, _ := tr.Update(n, frame); ok {
		t.Error("same nowcast computed twice")
	}
}

func TestSignalsAtMatchesTheField(t *testing.T) {
	n := movingRain(t)
	in := Inputs{
		POH:    storm(constant(0), 60, 100, 0.9),
		VIL:    storm(storm(constant(0), 60, 100, 30), 2, 3, 12),
		ETM:    storm(constant(0), 60, 100, 11000),
		VILOld: storm(constant(0), 56, 100, 5),
		ETMOld: storm(constant(0), 56, 100, 6000),
	}
	f := NewSignalField(n, in)
	same := func(a, b float32) bool { return a == b || (a != a && b != b) }
	for _, p := range [][2]int{{60, 100}, {68, 100}, {64, 97}, {40, 100}, {0, 0}, {3, 2}, {w - 1, h - 1}} {
		got, want := SignalsAt(n, in, p[0], p[1]), f.Pixel(p[1]*w+p[0])
		for j := range NumSignals {
			if !same(got.At[j], want.At[j]) || !same(got.Near[j], want.Near[j]) {
				t.Errorf("pixel %v signal %d: at %v/%v, near %v/%v", p, j, got.At[j], want.At[j], got.Near[j], want.Near[j])
			}
		}
		if got.Prob() != want.Prob() {
			t.Errorf("pixel %v: probability %v, field %v", p, got.Prob(), want.Prob())
		}
	}
}
