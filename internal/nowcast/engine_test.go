package nowcast

import (
	"math"
	"testing"

	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

// engineFrames returns two SRI frames of the cropped real file in testdata/
// and a "hail" frame at the latest time. The same grid stands in for hail:
// only the plumbing is under test here.
func engineFrames(t *testing.T) (sri []*store.Frame, hail *store.Frame) {
	g, err := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	t1 := base.Add(step)
	sri = []*store.Frame{
		{Product: "SRI", Time: base, Period: step, Grid: g},
		{Product: "SRI", Time: t1, Period: step, Grid: g},
	}
	return sri, &store.Frame{Product: "POH", Time: t1, Period: step, Grid: g}
}

func newEngine() *Engine {
	nd := func(v float64) bool { return v == -9999 }
	return &Engine{Steps: 3, Options: DefaultOptions, IsNoData: nd, HailIsNoData: nd}
}

// rainy is a pixel of sri_crop.tif with 2.02 mm/h.
const rainyLat, rainyLon = 45.78886, 6.01149

func TestEngineHailAfterRain(t *testing.T) {
	sri, hail := engineFrames(t)
	e := newEngine()
	if _, err := e.Update(sri); err != nil {
		t.Fatal(err)
	}
	pts, _ := e.Current().Forecast(rainyLat, rainyLon)
	if !math.IsNaN(pts[0].Hail) {
		t.Fatalf("hail %v before any hail frame", pts[0].Hail)
	}
	if err := e.SetHail(hail); err != nil {
		t.Fatal(err)
	}
	pts, _ = e.Current().Forecast(rainyLat, rainyLon)
	if math.Abs(pts[3].Hail-2.02) > 1e-6 {
		t.Errorf("hail at +15m = %v, want the moved hail value", pts[3].Hail)
	}
}

func TestEngineHailBeforeRain(t *testing.T) {
	sri, hail := engineFrames(t)
	e := newEngine()
	if err := e.SetHail(hail); err != nil {
		t.Fatal(err)
	}
	n, err := e.Update(sri)
	if err != nil {
		t.Fatal(err)
	}
	if n.Hail == nil {
		t.Fatal("pending hail frame not attached")
	}
}

func TestEngineHailTimeMismatch(t *testing.T) {
	sri, hail := engineFrames(t)
	hail.Time = hail.Time.Add(-step) // older than the rain frame
	e := newEngine()
	e.Update(sri)
	e.SetHail(hail)
	if e.Current().Hail != nil {
		t.Fatal("hail from another time attached")
	}
}

func TestEngineNeedsTwoFrames(t *testing.T) {
	sri, _ := engineFrames(t)
	if _, err := newEngine().Update(sri[:1]); err != ErrNotEnoughFrames {
		t.Fatalf("err = %v", err)
	}
}
