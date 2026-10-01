// Command nowcastverify measures how good the rain nowcast is on real data.
//
// It downloads the SRI frames of the last hour and a half, runs the nowcast
// from frames that are an hour old, and compares each forecast step with what
// the radar actually observed. As a reference it scores "persistence", the
// naive forecast where rain stays where it is: a useful nowcast must beat it.
//
//	go run ./cmd/nowcastverify [--lead 60m] [--end 2026-10-01T14:00:00Z]
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

const userAgent = "radarpoint-nowcastverify/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	lead := flag.Duration("lead", time.Hour, "longest lead time to verify")
	history := flag.Int("history", 6, "frames used to estimate the motion")
	endFlag := flag.String("end", "", "time of the last observation (RFC 3339); default: latest available")
	flag.Parse()
	if err := run(*lead, *history, *endFlag); err != nil {
		fmt.Fprintln(os.Stderr, "nowcastverify:", err)
		os.Exit(1)
	}
}

func run(lead time.Duration, history int, endFlag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := dpc.New(userAgent)
	info, _ := dpc.Lookup("SRI")

	end, step := time.Time{}, 5*time.Minute
	if endFlag != "" {
		t, err := time.Parse(time.RFC3339, endFlag)
		if err != nil {
			return err
		}
		end = t.UTC().Truncate(step)
	} else {
		last, err := c.FindLast(ctx, "SRI")
		if err != nil {
			return err
		}
		end = last.Time
	}
	steps := int(lead / step)
	base := end.Add(-time.Duration(steps) * step)
	first := base.Add(-time.Duration(history-1) * step)

	// Download every frame from first to end; missing ones are skipped.
	var grid *raster.GeoTIFF
	fields := map[time.Time]*nowcast.Field{}
	for t := first; !t.After(end); t = t.Add(step) {
		g, err := download(ctx, c, t)
		if errors.Is(err, dpc.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "missing %s\n", t.Format("15:04"))
			continue
		}
		if err != nil {
			return err
		}
		f, err := nowcast.NewField(g, info.IsNoData)
		if err != nil {
			return err
		}
		grid, fields[t] = g, f
	}

	var in []nowcast.Frame
	for t := first; !t.After(base); t = t.Add(step) {
		if f := fields[t]; f != nil {
			in = append(in, nowcast.Frame{Time: t, Field: f})
		}
	}
	if fields[base] == nil {
		return fmt.Errorf("no frame at the base time %s", base.Format(time.RFC3339))
	}
	n, err := nowcast.Compute(in, grid.Projection, grid.Transform, step, steps, nowcast.DefaultOptions)
	if err != nil {
		return err
	}

	fmt.Printf("Base %s UTC, %d frame pairs, %d of %d blocks tracked\n",
		base.Format("2006-01-02 15:04"), n.Pairs, n.Motion.Measured, n.Motion.NX*n.Motion.NY)
	fmt.Printf("Rain at base: %.1f%% of covered pixels ≥ 0.5 mm/h\n\n", rainShare(fields[base], 0.5))
	fmt.Println("CSI (critical success index, 1 = perfect, 0 = no skill); higher is better")
	fmt.Println("lead    ≥0.5 mm/h  nowcast  persist.    ≥5 mm/h  nowcast  persist.")
	for k := 1; k <= steps; k++ {
		obs := fields[base.Add(time.Duration(k)*step)]
		if obs == nil {
			continue
		}
		fc := n.ForecastField(k)
		fmt.Printf("+%2dm              %7.3f  %8.3f               %7.3f  %8.3f\n", k*5,
			csi(fc, obs, 0.5), csi(fields[base], obs, 0.5),
			csi(fc, obs, 5), csi(fields[base], obs, 5))
	}
	fmt.Println("\nData: " + dpc.Attribution)
	return nil
}

func download(ctx context.Context, c *dpc.Client, t time.Time) (*raster.GeoTIFF, error) {
	dl, err := c.DownloadURL(ctx, "SRI", t)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := c.Fetch(ctx, dl.URL, &buf); err != nil {
		return nil, err
	}
	im, err := raster.Parse(buf.Bytes())
	if err != nil {
		return nil, err
	}
	return raster.NewGeoTIFF(im)
}

// csi is hits / (hits + misses + false alarms) for rain ≥ threshold, over
// pixels where both fields have data. It is NaN when neither has rain.
func csi(fc, obs *nowcast.Field, threshold float32) float64 {
	var hits, misses, falseAlarms int
	for i, o := range obs.V {
		f := fc.V[i]
		if o != o || f != f { // NaN
			continue
		}
		switch fr, or := f >= threshold, o >= threshold; {
		case fr && or:
			hits++
		case or:
			misses++
		case fr:
			falseAlarms++
		}
	}
	if hits+misses+falseAlarms == 0 {
		return math.NaN()
	}
	return float64(hits) / float64(hits+misses+falseAlarms)
}

func rainShare(f *nowcast.Field, threshold float32) float64 {
	var rain, valid int
	for _, v := range f.V {
		if v == v {
			valid++
			if v >= threshold {
				rain++
			}
		}
	}
	return 100 * float64(rain) / float64(max(valid, 1))
}
