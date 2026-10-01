// Package ingest keeps a store up to date with the latest Radar-DPC products.
//
// Each product is downloaded once, whatever the number of users: the API is
// queried only when a new instant is due, then once per poll interval until
// it is published.
package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

// Poller follows one product type.
type Poller struct {
	Client  *dpc.Client
	Store   *store.Store
	Product string
	// Window is how much history to keep, e.g. 30 minutes. At least the
	// latest frame is always kept, whatever the product period.
	Window time.Duration
	Log    *slog.Logger

	now func() time.Time // for tests
	// missing remembers instants the API answered 404 for, so they are not
	// requested again on every poll.
	missing map[time.Time]bool
}

// Run polls until ctx is cancelled. On start it also downloads the past
// frames that fall within Window.
func (p *Poller) Run(ctx context.Context) error {
	for {
		wait, err := p.Poll(ctx)
		if err != nil && ctx.Err() == nil {
			p.log().Warn("update failed", "product", p.Product, "err", err, "retry_in", wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Poll asks for the latest instant and downloads every missing frame within
// the window. It returns how long to wait before the next call.
func (p *Poller) Poll(ctx context.Context) (time.Duration, error) {
	last, err := p.Client.FindLast(ctx, p.Product)
	if err != nil {
		return time.Minute, err
	}
	period, err := last.PeriodDuration()
	if err != nil {
		return time.Minute, err
	}

	keep := p.keep(period)
	var errs []error
	oldest := last.Time.Add(-time.Duration(keep-1) * period)
	if p.missing == nil {
		p.missing = map[time.Time]bool{}
	}
	p.Store.DropBefore(p.Product, oldest)
	for t := range p.missing {
		if t.Before(oldest) {
			delete(p.missing, t)
		}
	}
	for t := oldest; !t.After(last.Time); t = t.Add(period) {
		if p.missing[t] || p.Store.Has(p.Product, t) {
			continue
		}
		f, err := p.fetch(ctx, t, period)
		switch {
		case errors.Is(err, dpc.ErrNotFound):
			p.missing[t] = true
			p.log().Info("instant not published, skipping", "product", p.Product, "time", t)
		case err != nil:
			errs = append(errs, fmt.Errorf("%s %s: %w", p.Product, t.Format(time.RFC3339), err))
		default:
			p.Store.Add(f)
			p.log().Info("new frame", "product", p.Product, "time", t, "bytes", f.Size)
		}
	}
	if len(errs) > 0 {
		return time.Minute, errors.Join(errs...)
	}
	return p.nextWait(last.Time, period), nil
}

// keep is the number of frames covering the window, at least one.
func (p *Poller) keep(period time.Duration) int {
	return max(1, int(p.Window/period))
}

// nextWait schedules the next poll: not before the next instant is due,
// then at regular intervals until it shows up. Radar-DPC publishes a few
// minutes after the nominal time (tens of minutes for TEMP).
func (p *Poller) nextWait(latest time.Time, period time.Duration) time.Duration {
	interval := min(max(period/5, 30*time.Second), 5*time.Minute)
	due := latest.Add(period)
	if wait := due.Sub(p.clock()); wait > interval {
		return wait
	}
	return interval
}

func (p *Poller) fetch(ctx context.Context, t time.Time, period time.Duration) (*store.Frame, error) {
	dl, err := p.Client.DownloadURL(ctx, p.Product, t)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := p.Client.Fetch(ctx, dl.URL, &buf); err != nil {
		return nil, err
	}
	im, err := raster.Parse(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("GeoTIFF %s: %w", dl.Key, err)
	}
	g, err := raster.NewGeoTIFF(im)
	if err != nil {
		return nil, fmt.Errorf("GeoTIFF %s: %w", dl.Key, err)
	}
	return &store.Frame{
		Product: p.Product,
		Time:    t,
		Period:  period,
		Key:     dl.Key,
		Size:    buf.Len(),
		Fetched: p.clock(),
		Grid:    g,
	}, nil
}

func (p *Poller) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *Poller) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}
