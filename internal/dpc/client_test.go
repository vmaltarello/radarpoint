package dpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a client at srv with no retry delay.
func newTestClient(srv *httptest.Server) *Client {
	c := New("radarpoint-test")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	c.RetryWait = time.Millisecond
	return c
}

func checkHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Origin"); got != DefaultOrigin {
		t.Errorf("Origin = %q", got)
	}
	if got := r.Header.Get("Referer"); got != DefaultOrigin+"/" {
		t.Errorf("Referer = %q", got)
	}
	if got := r.Header.Get("User-Agent"); got != "radarpoint-test" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestFindLast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/findLastProductByType" || r.URL.Query().Get("type") != "SRI" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		io.WriteString(w, `{"total":1,"lastProducts":[{"productType":"SRI","time":1758706200000,"period":"PT5M"}]}`)
	}))
	defer srv.Close()

	p, err := newTestClient(srv).FindLast(context.Background(), "SRI")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 9, 24, 9, 30, 0, 0, time.UTC)
	if p.Type != "SRI" || !p.Time.Equal(want) || p.Period != "PT5M" {
		t.Fatalf("got %+v, want SRI %v PT5M", p, want)
	}
}

func TestFindLastEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"total":0,"lastProducts":[]}`)
	}))
	defer srv.Close()
	if _, err := newTestClient(srv).FindLast(context.Background(), "SRI"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDownloadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/downloadProduct" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var body struct {
			ProductType string `json:"productType"`
			ProductDate int64  `json:"productDate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ProductType != "SRI" || body.ProductDate != 1758706200000 {
			t.Errorf("body %+v, err %v", body, err)
		}
		io.WriteString(w, `{"bucket":"dpc-radar","key":"SRI/22-09-2025-14-20.tif","url":"https://dpc-radar.s3.eu-south-1.amazonaws.com/x","expiresSeconds":900}`)
	}))
	defer srv.Close()

	d, err := newTestClient(srv).DownloadURL(context.Background(), "SRI", time.UnixMilli(1758706200000))
	if err != nil {
		t.Fatal(err)
	}
	if d.Bucket != "dpc-radar" || d.Key != "SRI/22-09-2025-14-20.tif" || d.URL != "https://dpc-radar.s3.eu-south-1.amazonaws.com/x" || d.Expires != 15*time.Minute {
		t.Fatalf("got %+v", d)
	}
}

func TestDownloadURLErrors(t *testing.T) {
	for _, c := range []struct {
		status   int
		body     string
		notFound bool
		msg      string
	}{
		{404, `{"error":"File non trovato","bucket":"dpc-radar","key":"VMI/22-09-2025-14-20.tif"}`, true, "File non trovato"},
		{400, `{"error":"Campo 'productDate' (epoch ms) mancante o non numerico."}`, false, "productDate"},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		_, err := newTestClient(srv).DownloadURL(context.Background(), "VMI", time.UnixMilli(1758541200000))
		srv.Close()

		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != c.status || !strings.Contains(apiErr.Message, c.msg) {
			t.Errorf("%d: err = %v", c.status, err)
		}
		if errors.Is(err, ErrNotFound) != c.notFound {
			t.Errorf("%d: errors.Is(ErrNotFound) = %v", c.status, !c.notFound)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("%d: %d calls, client errors must not be retried", c.status, n)
		}
	}
}

func TestRetryOnceOnServerError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"total":1,"lastProducts":[{"productType":"POH","time":1758706200000,"period":"PT5M"}]}`)
	}))
	defer srv.Close()
	if _, err := newTestClient(srv).FindLast(context.Background(), "POH"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d calls, want 2", n)
	}
}

func TestRetryGivesUpAfterOneRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := newTestClient(srv).FindLast(context.Background(), "SRI")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d calls, want 2", n)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)
	c.HTTP.Timeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := c.FindLast(context.Background(), "SRI"); err == nil {
		t.Fatal("expected timeout error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v, timeout not applied", d)
	}
}

func TestFetch(t *testing.T) {
	payload := bytes.Repeat([]byte("tiff"), 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			t.Error("pre-signed S3 download must not carry API headers")
		}
		w.Write(payload)
	}))
	defer srv.Close()
	var buf bytes.Buffer
	n, err := newTestClient(srv).Fetch(context.Background(), srv.URL+"/SRI/x.tif?sig=1", &buf)
	if err != nil || n != int64(len(payload)) || !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestFetchExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
	}))
	defer srv.Close()
	var apiErr *APIError
	if _, err := newTestClient(srv).Fetch(context.Background(), srv.URL, io.Discard); !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("err = %v", err)
	}
}
