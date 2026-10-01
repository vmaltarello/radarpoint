package nowcast

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/store"
)

// Engine recomputes the nowcast whenever new frames arrive and keeps the
// latest result for concurrent readers.
type Engine struct {
	Steps    int // forecast steps, e.g. 12 for one hour of 5-minute steps
	Options  Options
	IsNoData func(float64) bool

	mu      sync.Mutex // serialises Update
	fields  map[time.Time]*Field
	current atomic.Pointer[Nowcast]
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
	n, err := Compute(in, latest.Grid.Projection, latest.Grid.Transform, latest.Period, e.Steps, e.Options)
	if err != nil {
		return nil, err
	}
	e.current.Store(n)
	return n, nil
}
