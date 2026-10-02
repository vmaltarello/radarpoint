package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/irene"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

// nowcastServer builds a nowcast from two copies of the cropped SRI file, so
// the rain does not move and the forecast equals the observation.
func nowcastServer(t *testing.T) *Server {
	g, err := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	sri, _ := dpc.Lookup("SRI")
	// No smoothing: these tests follow values through the API unchanged.
	o := nowcast.DefaultOptions
	o.SmoothPerStep = 0
	e := &nowcast.Engine{Steps: 12, Options: o, IsNoData: sri.IsNoData}
	frames := []*store.Frame{
		{Product: "SRI", Time: t0.Add(-5 * time.Minute), Period: 5 * time.Minute, Grid: g},
		{Product: "SRI", Time: t0, Period: 5 * time.Minute, Grid: g},
	}
	if _, err := e.Update(frames); err != nil {
		t.Fatal(err)
	}
	s := newServer(t)
	s.Nowcast = e.Current
	return s
}

func TestNowcast(t *testing.T) {
	s := nowcastServer(t)
	var b NowcastBody
	get(t, s, "/v1/nowcast?lat=45.78886&lon=6.01149", http.StatusOK, &b)
	if b.Status != StatusOK || b.BaseTime == nil || !b.BaseTime.Equal(t0) || len(b.Steps) != 13 || b.Attribution == "" {
		t.Fatalf("nowcast %+v", b)
	}
	last := b.Steps[12]
	if b.Steps[0].LeadMinutes != 0 || last.LeadMinutes != 60 || !last.Time.Equal(t0.Add(time.Hour)) {
		t.Errorf("leads %d…%d, last time %v", b.Steps[0].LeadMinutes, last.LeadMinutes, last.Time)
	}
	// Static rain: every step repeats the observed 2.02 mm/h.
	for _, st := range b.Steps {
		if st.Status != StatusOK || st.Value == nil || *st.Value != 2.02 {
			t.Errorf("step +%d: %s %v", st.LeadMinutes, st.Status, deref(st.Value))
		}
	}
	for _, st := range b.Steps {
		if st.RainProbability == nil || *st.RainProbability <= 0 || *st.RainProbability > 100 {
			t.Errorf("step +%d rain_probability %v, want a percentage above 0 in the rain", st.LeadMinutes, deref(st.RainProbability))
		}
	}
	if b.Steps[0].HailPercent != nil {
		t.Errorf("hail_percent %v without a hail frame", *b.Steps[0].HailPercent)
	}
	if b.Motion == nil || b.Motion.SpeedKmh != 0 {
		t.Errorf("motion %+v, want 0 km/h", b.Motion)
	}

	get(t, s, "/v1/nowcast?lat=41.9&lon=12.5", http.StatusOK, &b)
	if b.Status != StatusOutside || len(b.Steps) != 0 {
		t.Errorf("outside: %+v", b)
	}
}

func TestNowcastUnavailable(t *testing.T) {
	s := newServer(t)
	s.Nowcast = func() *nowcast.Nowcast { return nil }
	var b NowcastBody
	get(t, s, "/v1/nowcast?lat=45.6&lon=8.9", http.StatusOK, &b)
	if b.Status != StatusUnavailable || b.BaseTime != nil {
		t.Errorf("%+v", b)
	}
}

func TestNowcastNotServedWithoutEngine(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nowcast?lat=45.6&lon=8.9", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestNowcastHail(t *testing.T) {
	s := nowcastServer(t)
	g, _ := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	poh, _ := dpc.Lookup("POH")
	// Stand-in hail field: the SRI crop, whose rainy pixel holds 2.02, i.e.
	// 202% once scaled; only the plumbing and the scaling are checked.
	withHail := s.Nowcast().WithHail(mustField(t, g, poh.IsNoData))
	s.Nowcast = func() *nowcast.Nowcast { return withHail }

	var b NowcastBody
	get(t, s, "/v1/nowcast?lat=45.78886&lon=6.01149", http.StatusOK, &b)
	for _, st := range b.Steps {
		if st.HailPercent == nil || *st.HailPercent != 202 {
			t.Fatalf("step +%d hail_percent %v, want 202", st.LeadMinutes, deref(st.HailPercent))
		}
	}
}

func mustField(t *testing.T, g *raster.GeoTIFF, nd func(float64) bool) *nowcast.Field {
	f, err := nowcast.NewField(g, nd)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// constFields returns steps fields of the nowcast grid filled with v.
func constFields(n *nowcast.Nowcast, steps int, v float32) []*nowcast.Field {
	var out []*nowcast.Field
	for range steps {
		f := &nowcast.Field{W: n.Latest.W, H: n.Latest.H, V: make([]float32, len(n.Latest.V))}
		for i := range f.V {
			f.V[i] = v
		}
		out = append(out, f)
	}
	return out
}

func TestNowcastIRENE(t *testing.T) {
	s := nowcastServer(t)
	n := s.Nowcast()
	fc := &irene.Forecast{Base: n.Base, Step: n.Step, Members: 4,
		Mean: constFields(n, 12, 9), Prob: constFields(n, 12, 0.75)}
	s.Irene = func() *irene.Forecast { return fc }
	const rain = "/v1/nowcast?lat=45.78886&lon=6.01149"

	var b NowcastBody
	get(t, s, rain, http.StatusOK, &b)
	if b.Method != "irene" || b.Members != 4 {
		t.Fatalf("method %q members %d, want irene 4", b.Method, b.Members)
	}
	if *b.Steps[0].Value != 2.02 { // lead 0 is still the observation
		t.Errorf("lead 0 %v, want the observed 2.02", *b.Steps[0].Value)
	}
	if st := b.Steps[5]; *st.Value != 9 || *st.RainProbability != 75 {
		t.Errorf("+25 min: %v mm/h, %v%%; want IRENE's 9 and 75", *st.Value, *st.RainProbability)
	}

	get(t, s, rain+"&method=extrapolation", http.StatusOK, &b)
	if b.Method != "lagrangian-persistence" || *b.Steps[5].Value == 9 {
		t.Errorf("forced extrapolation: %q %v", b.Method, *b.Steps[5].Value)
	}

	// IRENE still on the previous frame: fall back, or 503 if asked for.
	old := *fc
	old.Base = n.Base.Add(-n.Step)
	s.Irene = func() *irene.Forecast { return &old }
	get(t, s, rain, http.StatusOK, &b)
	if b.Method != "lagrangian-persistence" {
		t.Errorf("stale IRENE used: %q", b.Method)
	}
	var e struct{ Status int }
	get(t, s, rain+"&method=irene", http.StatusServiceUnavailable, &e)

	// No radar now at the point: no data, as with the extrapolation.
	s.Irene = func() *irene.Forecast { return fc }
	get(t, s, "/v1/nowcast?lat=46.02527&lon=4.75645", http.StatusOK, &b)
	if b.Steps[3].Status != StatusNoData || b.Steps[3].Value != nil {
		t.Errorf("uncovered point: %+v", b.Steps[3])
	}
}

func TestNowcastHailRisk(t *testing.T) {
	s := nowcastServer(t)
	n := s.Nowcast()
	const point = "/v1/nowcast?lat=45.78886&lon=6.01149"
	var b NowcastBody
	get(t, s, point, http.StatusOK, &b)
	if b.HailRisk != nil {
		t.Fatalf("hail risk %v without a hail model", *b.HailRisk)
	}

	risk := &hailrisk.Risk{Base: n.Base, Prob: constFields(n, 1, 0.237)[0]}
	s.HailRisk = func() *hailrisk.Risk { return risk }
	b = NowcastBody{}
	get(t, s, point, http.StatusOK, &b)
	if b.HailRisk == nil || *b.HailRisk != 24 {
		t.Fatalf("hail risk %v, want 24 (%%)", b.HailRisk)
	}

	// A risk computed for an older radar frame is not reported.
	risk = &hailrisk.Risk{Base: n.Base.Add(-n.Step), Prob: risk.Prob}
	b = NowcastBody{}
	get(t, s, point, http.StatusOK, &b)
	if b.HailRisk != nil {
		t.Errorf("hail risk %v from an older frame", *b.HailRisk)
	}
}
