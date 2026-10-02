package irene

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// ErrGap means the last frames are not 6 consecutive instants, which IRENE
// needs.
var ErrGap = errors.New("irene: the last 6 frames are not consecutive")

// Runner asks IRENE for a forecast whenever a new nowcast is available,
// one at a time, in the background, and keeps the latest result.
type Runner struct {
	Client *Client
	Steps  int
	// Done, if set, is called after each attempt, with the forecast or the
	// error.
	Done func(*Forecast, error)

	running atomic.Bool
	last    atomic.Pointer[time.Time] // base of the latest request
	current atomic.Pointer[Forecast]
}

// Current returns the latest forecast, or nil.
func (r *Runner) Current() *Forecast { return r.current.Load() }

// Request starts a forecast for the frames of n unless one is running or
// was already made for the same base time. It returns at once.
func (r *Runner) Request(ctx context.Context, n *nowcast.Nowcast) {
	if n == nil {
		return
	}
	if last := r.last.Load(); last != nil && last.Equal(n.Base) {
		return
	}
	past, err := lastFrames(n)
	if err != nil {
		r.done(nil, err)
		return
	}
	if !r.running.CompareAndSwap(false, true) {
		return // the next nowcast will ask again
	}
	base := n.Base
	r.last.Store(&base)
	go func() {
		defer r.running.Store(false)
		fc, err := r.Client.Forecast(ctx, past, n.Base, n.Step, r.Steps)
		if err == nil {
			r.current.Store(fc)
		}
		r.done(fc, err)
	}()
}

func (r *Runner) done(fc *Forecast, err error) {
	if r.Done != nil {
		r.Done(fc, err)
	}
}

// lastFrames returns the fields of the last PastFrames observations of n,
// oldest first, if they are consecutive.
func lastFrames(n *nowcast.Nowcast) ([]*nowcast.Field, error) {
	obs := n.Observed
	if len(obs) < PastFrames {
		return nil, ErrGap
	}
	obs = obs[len(obs)-PastFrames:]
	past := make([]*nowcast.Field, PastFrames)
	for i, f := range obs {
		if i > 0 && f.Time.Sub(obs[i-1].Time) != n.Step {
			return nil, ErrGap
		}
		past[i] = f.Field
	}
	return past, nil
}
