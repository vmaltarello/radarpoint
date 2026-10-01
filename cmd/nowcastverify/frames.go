package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

// source downloads SRI frames, keeping a copy of each file in a cache
// directory so that repeated runs do not hit Radar-DPC again.
type source struct {
	client *dpc.Client
	dir    string // "" disables the disk cache
	info   dpc.Info

	grid    *raster.GeoTIFF // georeference of the last frame read
	fields  map[time.Time]*nowcast.Field
	missing map[time.Time]bool
	fetched int // files downloaded during this run
}

func newSource(c *dpc.Client, dir string) *source {
	info, _ := dpc.Lookup("SRI")
	return &source{client: c, dir: dir, info: info,
		fields: map[time.Time]*nowcast.Field{}, missing: map[time.Time]bool{}}
}

// field returns the decoded frame at t, or nil if Radar-DPC has none.
func (s *source) field(ctx context.Context, t time.Time) (*nowcast.Field, error) {
	if f, ok := s.fields[t]; ok {
		return f, nil
	}
	if s.missing[t] {
		return nil, nil
	}
	b, err := s.file(ctx, t)
	if errors.Is(err, dpc.ErrNotFound) {
		s.missing[t] = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	im, err := raster.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("SRI %s: %w", t.Format(time.RFC3339), err)
	}
	g, err := raster.NewGeoTIFF(im)
	if err != nil {
		return nil, fmt.Errorf("SRI %s: %w", t.Format(time.RFC3339), err)
	}
	f, err := nowcast.NewField(g, s.info.IsNoData)
	if err != nil {
		return nil, err
	}
	s.grid, s.fields[t] = g, f
	return f, nil
}

// file returns the GeoTIFF bytes at t, from the cache or from Radar-DPC.
func (s *source) file(ctx context.Context, t time.Time) ([]byte, error) {
	path := ""
	if s.dir != "" {
		path = filepath.Join(s.dir, "SRI_"+t.UTC().Format("200601021504")+".tif")
		if b, err := os.ReadFile(path); err == nil {
			return b, nil
		}
	}
	dl, err := s.client.DownloadURL(ctx, "SRI", t)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := s.client.Fetch(ctx, dl.URL, &buf); err != nil {
		return nil, err
	}
	s.fetched++
	if path != "" {
		if err := os.MkdirAll(s.dir, 0o755); err == nil {
			os.WriteFile(path, buf.Bytes(), 0o644) // best effort: the cache is optional
		}
	}
	return buf.Bytes(), nil
}

// forget drops decoded frames before t to bound memory on long runs.
func (s *source) forget(t time.Time) {
	for k := range s.fields {
		if k.Before(t) {
			delete(s.fields, k)
		}
	}
}
