package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/store"
)

// fakeDPC imitates the Radar-DPC API and S3, serving a real (cropped) file
// for every instant except the ones marked missing.
type fakeDPC struct {
	t       *testing.T
	srv     *httptest.Server
	file    []byte
	mu      sync.Mutex
	last    time.Time
	period  string
	missing map[int64]bool
	asked   []time.Time // instants requested from /downloadProduct
}

func newFakeDPC(t *testing.T, last time.Time, period string) *fakeDPC {
	file, err := os.ReadFile("../../testdata/sri_crop.tif")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDPC{t: t, file: file, last: last, period: period, missing: map[int64]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /findLastProductByType", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprintf(w, `{"total":1,"lastProducts":[{"productType":%q,"time":%d,"period":%q}]}`,
			r.URL.Query().Get("type"), f.last.UnixMilli(), f.period)
	})
	mux.HandleFunc("POST /downloadProduct", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProductDate int64 `json:"productDate"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, time.UnixMilli(body.ProductDate).UTC())
		if f.missing[body.ProductDate] {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"File non trovato"}`)
			return
		}
		fmt.Fprintf(w, `{"bucket":"b","key":"SRI/%d.tif","url":"%s/s3/%d","expiresSeconds":300}`,
			body.ProductDate, f.srv.URL, body.ProductDate)
	})
	mux.HandleFunc("GET /s3/", func(w http.ResponseWriter, r *http.Request) { w.Write(f.file) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDPC) poller(st *store.Store, now time.Time) *Poller {
	c := dpc.New("test")
	c.BaseURL = f.srv.URL
	c.RetryWait = time.Millisecond
	return &Poller{
		Client: c, Store: st, Product: "SRI", Window: 30 * time.Minute,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: func() time.Time { return now },
	}
}

func (f *fakeDPC) takeAsked() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.asked
	f.asked = nil
	return a
}

var t0 = time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)

func TestBackfillAndFollow(t *testing.T) {
	f := newFakeDPC(t, t0, "PT5M")
	f.missing[t0.Add(-10*time.Minute).UnixMilli()] = true // 12:20 never published
	st := store.New()
	p := f.poller(st, t0.Add(8*time.Minute))

	// First poll: the 6 instants of the 30-minute window, 12:05…12:30.
	wait, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.takeAsked()); n != 6 {
		t.Fatalf("asked %d instants, want 6", n)
	}
	frames := st.Frames("SRI")
	if len(frames) != 5 || !frames[0].Time.Equal(t0.Add(-25*time.Minute)) || !frames[4].Time.Equal(t0) {
		t.Fatalf("got %d frames %v…", len(frames), frames[0].Time)
	}
	if frames[0].Grid == nil || frames[0].Period != 5*time.Minute {
		t.Fatalf("frame not decoded: %+v", frames[0])
	}
	// 12:35 is due before now+interval, so poll again after one interval.
	if wait != time.Minute {
		t.Errorf("wait = %v, want 1m", wait)
	}

	// Nothing new: no downloads, and the missing 12:20 is not asked again.
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := f.takeAsked(); len(a) != 0 {
		t.Fatalf("asked %v, want nothing", a)
	}

	// 12:35 appears: only that instant is downloaded, 12:05 is dropped.
	f.mu.Lock()
	f.last = t0.Add(5 * time.Minute)
	f.mu.Unlock()
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := f.takeAsked(); len(a) != 1 || !a[0].Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("asked %v, want only 12:35", a)
	}
	frames = st.Frames("SRI")
	if len(frames) != 5 || !frames[0].Time.Equal(t0.Add(-20*time.Minute)) {
		t.Fatalf("after update: %d frames from %v", len(frames), frames[0].Time)
	}
}

func TestHourlyProductKeepsLatest(t *testing.T) {
	f := newFakeDPC(t, t0.Truncate(time.Hour), "PT1H")
	st := store.New()
	p := f.poller(st, t0)
	wait, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A 30-minute window is shorter than the period: keep just the latest.
	if n := len(st.Frames("SRI")); n != 1 {
		t.Fatalf("%d frames, want 1", n)
	}
	// Next instant is due at 13:00, 30 minutes from now.
	if wait != 30*time.Minute {
		t.Errorf("wait = %v, want 30m", wait)
	}
}

func TestPollErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := dpc.New("test")
	c.BaseURL = srv.URL
	c.RetryWait = time.Millisecond
	p := &Poller{Client: c, Store: store.New(), Product: "SRI", Window: 30 * time.Minute,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wait, err := p.Poll(context.Background())
	if err == nil || wait != time.Minute {
		t.Fatalf("wait %v err %v; want 1m and an error", wait, err)
	}
}
