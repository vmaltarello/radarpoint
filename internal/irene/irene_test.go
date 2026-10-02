package irene

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/geo"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// fakeIRENE answers like the real service: the mean is the latest frame
// plus 1, the probability 50% everywhere.
func fakeIRENE(t *testing.T, calls *int, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*calls++
		mu.Unlock()
		q := r.URL.Query()
		if q.Get("members") != "4" || q.Get("steps") != "3" || q.Get("height") != "4" || q.Get("width") != "5" {
			t.Errorf("query %v", q)
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		if len(raw) != 6*4*5*4 {
			t.Fatalf("body %d bytes", len(raw))
		}
		latest := raw[5*4*5*4:]
		for range 3 {
			for i := range 20 {
				v := math.Float32frombits(binary.LittleEndian.Uint32(latest[4*i:]))
				binary.Write(w, binary.LittleEndian, v+1)
			}
			for range 20 {
				w.Write([]byte{50})
			}
		}
	}))
}

func field(v float32) *nowcast.Field {
	f := &nowcast.Field{W: 5, H: 4, V: make([]float32, 20)}
	for i := range f.V {
		f.V[i] = v
	}
	return f
}

func TestClientForecast(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := fakeIRENE(t, &calls, &mu)
	defer srv.Close()
	c := NewClient(srv.URL)
	past := []*nowcast.Field{field(0), field(0), field(0), field(0), field(0), field(2)}
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	fc, err := c.Forecast(context.Background(), past, base, 5*time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.Mean) != 3 || fc.Mean[2].V[7] != 3 || fc.Prob[0].V[0] != 0.5 || !fc.Base.Equal(base) {
		t.Fatalf("forecast %+v", fc)
	}
	if _, err := c.Forecast(context.Background(), past[:5], base, 5*time.Minute, 3); err == nil {
		t.Error("5 frames accepted")
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error": "busy"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	past := []*nowcast.Field{field(0), field(0), field(0), field(0), field(0), field(0)}
	if _, err := NewClient(srv.URL).Forecast(context.Background(), past, time.Now(), 5*time.Minute, 3); err == nil {
		t.Fatal("expected an error")
	}
}

func testNowcast(t *testing.T, times int, gap bool) *nowcast.Nowcast {
	var frames []nowcast.Frame
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for i := range times {
		ti := base.Add(time.Duration(i) * 5 * time.Minute)
		if gap && i == 2 {
			continue
		}
		frames = append(frames, nowcast.Frame{Time: ti, Field: field(float32(i))})
	}
	tm := &geo.TransverseMercator{Lat0: 42, Lon0: 12.5, K0: 1, Ellipsoid: geo.WGS84}
	gt := geo.GeoTransform{OriginX: 0, OriginY: 0, PixelW: 1000, PixelH: 1000}
	n, err := nowcast.Compute(frames, tm, gt, 5*time.Minute, 3, nowcast.DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunner(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := fakeIRENE(t, &calls, &mu)
	defer srv.Close()
	done := make(chan error, 4)
	r := &Runner{Client: NewClient(srv.URL), Steps: 3, Done: func(_ *Forecast, err error) { done <- err }}

	n := testNowcast(t, 7, false)
	r.Request(context.Background(), n)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if fc := r.Current(); fc == nil || !fc.Base.Equal(n.Base) || fc.Mean[0].V[0] != 7 { // latest frame is 6, +1
		t.Fatalf("current %+v", r.Current())
	}
	// Same base again: no new request.
	r.Request(context.Background(), n)
	mu.Lock()
	if calls != 1 {
		t.Errorf("%d calls, want 1", calls)
	}
	mu.Unlock()

	// A gap among the last 6 frames: IRENE is not asked.
	r.Request(context.Background(), testNowcast(t, 8, true)) // a later base, frames 1, 3, 4, 5, 6, 7
	if err := <-done; !errors.Is(err, ErrGap) {
		t.Errorf("gap: err = %v", err)
	}
}
