package hailrisk

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

// Tracker keeps the latest risk. Update recomputes it once all the inputs
// of a new nowcast are available, whatever order the products arrive in.
type Tracker struct {
	mu      sync.Mutex // serialises Update
	current atomic.Pointer[Risk]
}

// Current returns the latest risk, or nil before the first one.
func (t *Tracker) Current() *Risk { return t.current.Load() }

// Update computes the risk for nowcast n if it is new and frame returns
// every input: POH, VIL and ETM at n's base time, VIL and ETM GrowthSteps
// earlier. It reports whether a new risk was stored; missing inputs are not
// an error, the next call tries again.
func (t *Tracker) Update(n *nowcast.Nowcast, frame func(product string, at time.Time) *raster.GeoTIFF) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n == nil {
		return false, nil
	}
	if r := t.current.Load(); r != nil && r.Base.Equal(n.Base) {
		return false, nil
	}
	old := n.Base.Add(-GrowthSteps * n.Step)
	grids := []*raster.GeoTIFF{frame("POH", n.Base), frame("VIL", n.Base), frame("ETM", n.Base),
		frame("VIL", old), frame("ETM", old)}
	for _, g := range grids {
		if g == nil {
			return false, nil
		}
	}
	poh, _ := dpc.Lookup("POH")
	var in Inputs
	var err error
	if in.POH, err = nowcast.NewField(grids[0], poh.IsNoData); err != nil {
		return false, err
	}
	for i, dst := range []**nowcast.Field{&in.VIL, &in.ETM, &in.VILOld, &in.ETMOld} {
		if *dst, err = NewField(grids[i+1]); err != nil {
			return false, err
		}
	}
	for _, f := range []*nowcast.Field{in.POH, in.VIL, in.ETM, in.VILOld, in.ETMOld} {
		if f.W != n.Latest.W || f.H != n.Latest.H {
			return false, fmt.Errorf("hailrisk: %dx%d input on a %dx%d nowcast grid", f.W, f.H, n.Latest.W, n.Latest.H)
		}
	}
	t.current.Store(&Risk{Base: n.Base, Prob: Compute(n, in)})
	return true, nil
}
