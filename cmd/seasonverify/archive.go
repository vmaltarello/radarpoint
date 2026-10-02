package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/geo"
	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

const step = 5 * time.Minute

// The Radar-DPC composite grid: 1200×1400 pixels of 1 km in a transverse
// Mercator projection. The .npy cases of the IT-DPC-SRI archive are on it
// too; GeoTIFFs are checked against it.
var (
	dpcProj      = &geo.TransverseMercator{Lat0: 42, Lon0: 12.5, K0: 1, Ellipsoid: geo.WGS84}
	dpcTransform = geo.GeoTransform{OriginX: -600000, OriginY: 650000, PixelW: 1000, PixelH: 1000}
)

// archive reads the files written by dpcarchive:
// <dir>/<product>/<PRODUCT>_yyyymmddhhmm.tif.
type archive struct{ dir string }

func (a archive) path(product string, t time.Time) string {
	return filepath.Join(a.dir, strings.ToLower(product), product+"_"+t.UTC().Format("200601021504")+".tif")
}

func (a archive) exists(product string, t time.Time) bool {
	_, err := os.Stat(a.path(product, t))
	return err == nil
}

// complete reports whether every frame from t+first·step to t+last·step
// is in the archive.
func (a archive) complete(product string, t time.Time, first, last int) bool {
	for k := first; k <= last; k++ {
		if !a.exists(product, t.Add(time.Duration(k)*step)) {
			return false
		}
	}
	return true
}

func (a archive) grid(product string, t time.Time) *raster.GeoTIFF {
	g, err := raster.OpenGeoTIFF(a.path(product, t))
	if err != nil {
		return nil
	}
	if g.Transform != dpcTransform || g.Width != 1200 || g.Height != 1400 {
		fmt.Fprintf(os.Stderr, "seasonverify: %s is not on the Radar-DPC grid\n", a.path(product, t))
		os.Exit(1)
	}
	return g
}

// field reads one frame: SRI and POH with their catalog nodata, VIL and ETM
// as the hail model reads them. Nil if missing or unreadable.
func (a archive) field(product string, t time.Time) *nowcast.Field {
	g := a.grid(product, t)
	if g == nil {
		return nil
	}
	var f *nowcast.Field
	var err error
	switch product {
	case "VIL", "ETM":
		f, err = hailrisk.NewField(g)
	default:
		info, _ := dpc.Lookup(product)
		f, err = nowcast.NewField(g, info.IsNoData)
	}
	if err != nil {
		return nil
	}
	return f
}

// sriFrames returns the store frames of SRI from t-5·step to t, for an
// engine, or nil if one is missing.
func (a archive) sriFrames(t time.Time) []*store.Frame {
	var out []*store.Frame
	for k := -5; k <= 0; k++ {
		tk := t.Add(time.Duration(k) * step)
		g := a.grid("SRI", tk)
		if g == nil {
			return nil
		}
		out = append(out, &store.Frame{Product: "SRI", Time: tk, Period: step, Grid: g})
	}
	return out
}

// hailInputs returns the inputs of the hail model at t, or false if one is
// missing.
func (a archive) hailInputs(t time.Time) (hailrisk.Inputs, bool) {
	old := t.Add(-hailrisk.GrowthSteps * step)
	in := hailrisk.Inputs{POH: a.field("POH", t), VIL: a.field("VIL", t), ETM: a.field("ETM", t),
		VILOld: a.field("VIL", old), ETMOld: a.field("ETM", old)}
	ok := in.POH != nil && in.VIL != nil && in.ETM != nil && in.VILOld != nil && in.ETMOld != nil
	return in, ok
}

// newEngine returns a nowcast engine as radarpointd runs it.
func newEngine(steps int) *nowcast.Engine {
	sri, _ := dpc.Lookup("SRI")
	poh, _ := dpc.Lookup("POH")
	return &nowcast.Engine{Steps: steps, Options: nowcast.DefaultOptions, IsNoData: sri.IsNoData, HailIsNoData: poh.IsNoData}
}

// fractions returns the shares of pixels with data at or above each
// threshold; ok is false when less than a tenth of the grid has data.
func fractions(f *nowcast.Field, thr ...float32) (out []float64, ok bool) {
	out = make([]float64, len(thr))
	var n int
	for _, v := range f.V {
		if v != v {
			continue
		}
		n++
		for i, t := range thr {
			if v >= t {
				out[i]++
			}
		}
	}
	if n < len(f.V)/10 {
		return out, false
	}
	for i := range out {
		out[i] /= float64(n)
	}
	return out, true
}

// parallel runs f on every item with the given number of workers and
// returns the results in order.
func parallel[T, R any](items []T, workers int, f func(T) R) []R {
	out := make([]R, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, it := range items {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			out[i] = f(it)
		})
	}
	wg.Wait()
	return out
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), nil
}
