package grids

import (
	"bytes"
	"compress/gzip"
	"io"
	"math"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/irene"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

var base = time.Date(2026, 10, 1, 12, 45, 0, 0, time.UTC)

const step = 5 * time.Minute

// testNowcast builds a 3-step nowcast from two copies of the cropped SRI
// file in testdata/, so the rain does not move.
func testNowcast(t *testing.T) *nowcast.Nowcast {
	g, err := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	o := nowcast.DefaultOptions
	o.SmoothPerStep = 0
	e := &nowcast.Engine{Steps: 3, Options: o, IsNoData: func(v float64) bool { return v == -9999 }}
	n, err := e.Update([]*store.Frame{
		{Product: "SRI", Time: base.Add(-step), Period: step, Grid: g},
		{Product: "SRI", Time: base, Period: step, Grid: g},
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func constField(n *nowcast.Nowcast, v float32) *nowcast.Field {
	f := &nowcast.Field{W: n.Latest.W, H: n.Latest.H, V: make([]float32, len(n.Latest.V))}
	for i := range f.V {
		f.V[i] = v
	}
	return f
}

func decode(t *testing.T, gz []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestValues(t *testing.T) {
	rain := Values(Rain)
	if len(rain) != 255 || rain[0] != 0 || rain[1] != RainMin || rain[254] != RainMax {
		t.Fatalf("rain table: len %d, %v %v … %v", len(rain), rain[0], rain[1], rain[254])
	}
	for b := 2; b < 255; b++ {
		if r := rain[b] / rain[b-1]; r <= 1 || r > 1.04 {
			t.Fatalf("byte %d: %v after %v", b, rain[b], rain[b-1])
		}
	}
	for _, v := range []float32{0.1, 0.5, 2.02, 17, 120} {
		got := rain[rainByte(v)]
		if math.Abs(got-float64(v))/float64(v) > 0.02 {
			t.Errorf("%v mm/h decodes as %v", v, got)
		}
	}
	if rainByte(0.05) != 0 || rainByte(1000) != 254 || rainByte(float32(math.NaN())) != NoData {
		t.Error("rain bytes at the ends of the scale")
	}
	pct := Values(Hail)
	if pct[0] != 0 || pct[37] != 37 || pct[100] != 100 {
		t.Errorf("percent table %v %v %v", pct[0], pct[37], pct[100])
	}
	if percentByte(0.374) != 37 || percentByte(1.2) != 100 || percentByte(float32(math.NaN())) != NoData {
		t.Error("percent bytes")
	}
}

func TestBuilder(t *testing.T) {
	n := testNowcast(t)
	var b Builder
	if b.Update(Inputs{}) || b.Current() != nil {
		t.Fatal("set without a nowcast")
	}
	if !b.Update(Inputs{Nowcast: n}) {
		t.Fatal("first update not stored")
	}
	set := b.Current()
	if set.Method != Extrapolation || len(set.Frames) != 2+3 {
		t.Fatalf("method %s, %d frames, want extrapolation and 5", set.Method, len(set.Frames))
	}
	first, last := set.Frames[0], set.Frames[4]
	if first.Kind != Observed || first.LeadMinutes != -5 || last.Kind != Forecast || last.LeadMinutes != 15 {
		t.Errorf("frames %+v … %+v", first, last)
	}
	if _, ok := first.Keys[RainProb]; ok {
		t.Error("rain probability on an observed frame")
	}
	if _, ok := last.Keys[Hail]; ok {
		t.Error("hail without a hail field")
	}

	// The observation decodes back to the field, within the scale's step.
	raw := decode(t, set.Data(Rain, set.Frames[1].Keys[Rain]))
	if len(raw) != n.Latest.W*n.Latest.H {
		t.Fatalf("%d bytes, want %d", len(raw), n.Latest.W*n.Latest.H)
	}
	rain := Values(Rain)
	wet := 0
	for i, v := range n.Latest.V {
		switch {
		case v != v:
			if raw[i] != NoData {
				t.Fatalf("pixel %d: no data stored as %d", i, raw[i])
			}
		case v >= RainMin:
			wet++
			if got := rain[raw[i]]; math.Abs(got-float64(v))/float64(v) > 0.02 {
				t.Fatalf("pixel %d: %v mm/h decodes as %v", i, v, got)
			}
		}
	}
	if wet == 0 {
		t.Fatal("no rain in the test file")
	}
	// Static rain: the forecast repeats the observation.
	if !bytes.Equal(decode(t, set.Data(Rain, last.Keys[Rain])), raw) {
		t.Error("forecast of static rain differs from the observation")
	}

	if b.Update(Inputs{Nowcast: n}) {
		t.Error("same inputs encoded again")
	}

	// Hail, IRENE and the hail probability for the same frame.
	hn := n.WithHail(constField(n, 0.6))
	fc := &irene.Forecast{Base: n.Base, Step: n.Step, Members: 4}
	for range n.Steps {
		fc.Mean = append(fc.Mean, constField(n, 9))
		fc.Prob = append(fc.Prob, constField(n, 0.75))
	}
	risk := &hailrisk.Risk{Base: n.Base, Prob: constField(n, 0.2)}
	older := func(time.Time) *nowcast.Field { return constField(n, 0.4) }
	if !b.Update(Inputs{Nowcast: hn, Irene: fc, Risk: risk, ObservedHail: older}) {
		t.Fatal("new inputs not stored")
	}
	next := b.Current()
	if next.Method != IRENE || next.Members != 4 {
		t.Errorf("method %s members %d", next.Method, next.Members)
	}
	if next.Data(Rain, set.Frames[0].Keys[Rain]) == nil || &next.Data(Rain, set.Frames[0].Keys[Rain])[0] != &set.Data(Rain, set.Frames[0].Keys[Rain])[0] {
		t.Error("observed frame encoded again")
	}
	if k := next.Frames[4].Keys[Rain]; k == last.Keys[Rain] {
		t.Errorf("forecast key %s unchanged with IRENE", k)
	}
	covered := func(i int) bool { return n.Latest.V[i] == n.Latest.V[i] }
	// IRENE is cut to the radar coverage; the other inputs are stored as
	// they are.
	checkAll := func(name string, raw []byte, want byte) {
		t.Helper()
		for i, v := range raw {
			if covered(i) && v != want || !covered(i) && name == RainProb && v != NoData {
				t.Fatalf("%s pixel %d: %d, want %d", name, i, v, want)
			}
		}
	}
	checkAll(RainProb, decode(t, next.Data(RainProb, next.Frames[4].Keys[RainProb])), 75)
	checkAll(Hail, decode(t, next.Data(Hail, next.Frames[1].Keys[Hail])), 60)
	checkAll(Hail, decode(t, next.Data(Hail, next.Frames[0].Keys[Hail])), 40)
	if _, ok := next.Frames[0].Keys[HailRisk]; ok {
		t.Error("hail probability on an older observation")
	}
	checkAll(HailRisk, decode(t, next.Data(HailRisk, next.Frames[1].Keys[HailRisk])), 20)

	// Inputs for another frame are ignored.
	stale := &hailrisk.Risk{Base: n.Base.Add(-step), Prob: risk.Prob}
	oldFc := *fc
	oldFc.Base = stale.Base
	b.Update(Inputs{Nowcast: n, Irene: &oldFc, Risk: stale})
	if s := b.Current(); s.Method != Extrapolation || s.Frames[1].Keys[HailRisk] != "" {
		t.Errorf("inputs of another frame used: method %s, keys %v", s.Method, s.Frames[1].Keys)
	}
}
