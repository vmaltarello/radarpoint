package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
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
	e := &nowcast.Engine{Steps: 12, Options: nowcast.DefaultOptions, IsNoData: sri.IsNoData}
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
