package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 45, 0, 0, time.UTC)

// newServer serves SRI from the cropped real file in testdata/ and declares
// POH, for which nothing has been downloaded.
func newServer(t *testing.T) *Server {
	g, err := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	st := store.New()
	st.Add(&store.Frame{Product: "SRI", Time: t0.Add(-5 * time.Minute), Grid: g})
	st.Add(&store.Frame{Product: "SRI", Time: t0, Grid: g})
	return &Server{Store: st, Products: []string{"SRI", "POH"},
		Now: func() time.Time { return t0.Add(9 * time.Minute) }}
}

func get(t *testing.T, s *Server, url string, wantStatus int, out any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != wantStatus {
		t.Fatalf("%s: status %d, want %d: %s", url, rec.Code, wantStatus, rec.Body)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("%s: missing CORS header", url)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v", url, err)
	}
}

func TestNow(t *testing.T) {
	s := newServer(t)
	for _, c := range []struct {
		name     string
		url      string
		status   string
		value    *float64
		poh      string
		ageCheck bool
	}{
		{"zero rain", "/now?lat=45.5966&lon=8.915", StatusOK, new(float64), StatusUnavailable, true},
		{"rain", "/now?lat=45.78886&lon=6.01149", StatusOK, ptr(2.02), StatusUnavailable, false},
		{"no radar", "/now?lat=46.02527&lon=4.75645", StatusNoData, nil, StatusUnavailable, false},
		{"outside", "/now?lat=41.9&lon=12.5", StatusOutside, nil, StatusUnavailable, false},
	} {
		var resp PointBody
		get(t, s, c.url, http.StatusOK, &resp)
		if len(resp.Readings) != 2 || resp.Attribution == "" {
			t.Fatalf("%s: %+v", c.name, resp)
		}
		sri, poh := resp.Readings[0], resp.Readings[1]
		if sri.Product != "SRI" || sri.Status != c.status || !sameValue(sri.Value, c.value) {
			t.Errorf("%s: SRI %+v value %v, want %s %v", c.name, sri, deref(sri.Value), c.status, deref(c.value))
		}
		if sri.Time == nil || !sri.Time.Equal(t0) || *sri.AgeSeconds != 540 || sri.Unit != "mm/h" {
			t.Errorf("%s: SRI time/age/unit %v %v %q", c.name, sri.Time, sri.AgeSeconds, sri.Unit)
		}
		if poh.Status != c.poh || poh.Value != nil || poh.Time != nil {
			t.Errorf("%s: POH %+v", c.name, poh)
		}
	}
}

func TestHistory(t *testing.T) {
	var resp PointBody
	get(t, newServer(t), "/history?lat=45.5966&lon=8.915&product=SRI", http.StatusOK, &resp)
	if len(resp.Readings) != 2 || !resp.Readings[0].Time.Before(*resp.Readings[1].Time) {
		t.Fatalf("history %+v", resp.Readings)
	}
}

func TestBadRequests(t *testing.T) {
	s := newServer(t)
	for _, url := range []string{
		"/now", "/now?lat=x&lon=1", "/now?lat=91&lon=1", "/now?lat=NaN&lon=1",
		"/history?lat=45&lon=9", "/history?lat=45&lon=9&product=TEMP", "/history?lat=45&lon=9&product=sri",
	} {
		var e struct {
			Status int
			Detail string
		}
		get(t, s, url, http.StatusUnprocessableEntity, &e)
		if e.Status != http.StatusUnprocessableEntity || e.Detail == "" {
			t.Errorf("%s: error body %+v", url, e)
		}
	}
}

func TestOpenAPI(t *testing.T) {
	var doc struct {
		Paths map[string]any
	}
	get(t, newServer(t), "/openapi.json", http.StatusOK, &doc)
	for _, p := range []string{"/now", "/history", "/healthz"} {
		if doc.Paths[p] == nil {
			t.Errorf("OpenAPI lacks %s", p)
		}
	}
	// The product parameter lists exactly the products the server follows.
	raw, _ := json.Marshal(doc.Paths["/history"])
	if !strings.Contains(string(raw), `"enum":["SRI","POH"]`) {
		t.Errorf("history product enum missing: %s", raw)
	}
}

func TestHealth(t *testing.T) {
	var h struct {
		OK       bool
		Products map[string]struct{ Frames int }
	}
	// POH has no frames yet, so the service is not fully ready.
	get(t, newServer(t), "/healthz", http.StatusServiceUnavailable, &h)
	if h.OK || h.Products["SRI"].Frames != 2 || h.Products["POH"].Frames != 0 {
		t.Fatalf("health %+v", h)
	}
}

func ptr(v float64) *float64 { return &v }

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func sameValue(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
