package nowcast

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/store"
)

// Engine recomputes the nowcast whenever new frames arrive and keeps the
// latest result for concurrent readers. Rain frames drive the motion; an
// optional hail frame with the same time is moved along with the rain.
type Engine struct {
	Steps        int // forecast steps, e.g. 12 for one hour of 5-minute steps
	Options      Options
	IsNoData     func(float64) bool // nodata test for rain frames
	HailIsNoData func(float64) bool // nodata test for hail frames

	mu       sync.Mutex // serialises Update and SetHail
	fields   map[time.Time]*Field
	pairs    map[pairKey]*PairMotion // motion of frame pairs already measured
	hail     *Field                  // latest hail field and its time
	hailTime time.Time
	current  atomic.Pointer[Nowcast]
}

// Current returns the latest nowcast, or nil before the first one.
func (e *Engine) Current() *Nowcast { return e.current.Load() }

// Update builds a new nowcast from frames, all of the same product. Decoded
// grids are cached between calls, so each frame is decoded once.
func (e *Engine) Update(frames []*store.Frame) (*Nowcast, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(frames) < 2 {
		return nil, ErrNotEnoughFrames
	}
	if e.fields == nil {
		e.fields = map[time.Time]*Field{}
	}
	in := make([]Frame, 0, len(frames))
	keep := map[time.Time]bool{}
	for _, f := range frames {
		fld, ok := e.fields[f.Time]
		if !ok {
			var err error
			if fld, err = NewField(f.Grid, e.IsNoData); err != nil {
				return nil, err
			}
			e.fields[f.Time] = fld
		}
		keep[f.Time] = true
		in = append(in, Frame{Time: f.Time, Field: fld})
	}
	for t := range e.fields {
		if !keep[t] {
			delete(e.fields, t)
		}
	}
	latest := frames[len(frames)-1]
	if e.pairs == nil {
		e.pairs = map[pairKey]*PairMotion{}
	}
	for k := range e.pairs {
		if !keep[k.prev] || !keep[k.next] {
			delete(e.pairs, k)
		}
	}
	n, err := compute(in, latest.Grid.Projection, latest.Grid.Transform, latest.Period, e.Steps, e.Options, e.pairs)
	if err != nil {
		return nil, err
	}
	if e.hail != nil && e.hailTime.Equal(n.Base) {
		n.Hail = e.hail
	}
	e.current.Store(n)
	return n, nil
}

// SetHail provides the latest hail frame. It is attached to the current
// nowcast if their times match, and kept for the next Update otherwise, so
// rain and hail frames may arrive in any order.
func (e *Engine) SetHail(f *store.Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f == nil || (e.hail != nil && e.hailTime.Equal(f.Time)) {
		return nil
	}
	fld, err := NewField(f.Grid, e.HailIsNoData)
	if err != nil {
		return err
	}
	e.hail, e.hailTime = fld, f.Time
	if n := e.current.Load(); n != nil && n.Base.Equal(f.Time) {
		e.current.Store(n.WithHail(fld))
	}
	return nil
}
