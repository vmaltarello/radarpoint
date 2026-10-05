package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vmaltarello/radarpoint/internal/grids"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

func gridsServer(t *testing.T) *Server {
	s := nowcastServer(t)
	b := &grids.Builder{}
	b.Update(grids.Inputs{Nowcast: s.Nowcast()})
	s.Grids = b.Current
	return s
}

func TestGrids(t *testing.T) {
	s := gridsServer(t)
	var b GridsBody
	get(t, s, "/v1/grids", http.StatusOK, &b)
	if b.Status != StatusOK || b.BaseTime == nil || !b.BaseTime.Equal(t0) || b.Method != "extrapolation" || b.Attribution == "" {
		t.Fatalf("grids %+v", b)
	}
	if len(b.Frames) != 2+12 || b.Frames[0].LeadMinutes != -5 || b.Frames[13].LeadMinutes != 60 {
		t.Fatalf("%d frames, %d…%d min", len(b.Frames), b.Frames[0].LeadMinutes, b.Frames[len(b.Frames)-1].LeadMinutes)
	}
	g := b.Grid
	if g == nil || g.Projection.Name != "transverse_mercator" || g.PixelW != 1000 ||
		!strings.HasPrefix(g.Projection.Proj4, "+proj=tmerc +lat_0=42 +lon_0=12.5 +k=1 ") {
		t.Fatalf("grid %+v", g)
	}
	// The box holds the raster corners, and the curved edges only a little
	// beyond them.
	tif, err := raster.OpenGeoTIFF("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	minLat, maxLat, minLon, maxLon := 90.0, -90.0, 180.0, -180.0
	for _, c := range tif.Bounds() {
		minLat, maxLat = min(minLat, c[0]), max(maxLat, c[0])
		minLon, maxLon = min(minLon, c[1]), max(maxLon, c[1])
	}
	bb := g.Bounds
	if bb.South > minLat || bb.North < maxLat || bb.West > minLon || bb.East < maxLon ||
		bb.South < minLat-0.5 || bb.North > maxLat+0.5 || bb.West < minLon-0.5 || bb.East > maxLon+0.5 {
		t.Errorf("bounds %+v, corners lat %v…%v lon %v…%v", bb, minLat, maxLat, minLon, maxLon)
	}
	if l := b.Layers["rain"]; len(l.Values) != 255 || l.Unit != "mm/h" || l.Values[254] != grids.RainMax {
		t.Errorf("rain layer %+v", l)
	}

	url := b.Frames[1].Layers["rain"]
	rec := fetch(s, url, "gzip, deflate, br")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("%s: %d %v", url, rec.Code, rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	if len(raw) != g.Width*g.Height {
		t.Errorf("%d bytes, want %d", len(raw), g.Width*g.Height)
	}
	plain := fetch(s, url, "")
	if plain.Header().Get("Content-Encoding") != "" || !bytes.Equal(plain.Body.Bytes(), raw) {
		t.Errorf("without gzip: encoding %q, %d bytes", plain.Header().Get("Content-Encoding"), plain.Body.Len())
	}
	if fetch(s, url, "gzip;q=0").Header().Get("Content-Encoding") != "" {
		t.Error("gzip sent although refused")
	}
	if rec := fetch(s, "/v1/grids/rain/209901010000", "gzip"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown key: %d", rec.Code)
	}
	if rec := fetch(s, "/v1/grids/snow/"+b.Frames[1].Layers["rain"][len("/v1/grids/rain/"):], "gzip"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown layer: %d", rec.Code)
	}
}

func TestGridsUnavailable(t *testing.T) {
	s := newServer(t)
	s.Grids = func() *grids.Set { return nil }
	var b GridsBody
	get(t, s, "/v1/grids", http.StatusOK, &b)
	if b.Status != StatusUnavailable || b.Grid != nil || len(b.Frames) != 0 || len(b.Layers) != 4 {
		t.Errorf("grids %+v", b)
	}
}

func fetch(s *Server, url, acceptEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}
