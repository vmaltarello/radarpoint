// Command nowcastverify measures how good the rain nowcast is on real data.
//
// For each case it runs the nowcast from frames that are an hour old and
// compares every forecast step with what the radar then observed. As a
// reference it scores persistence, the naive forecast where rain stays where
// it is: a useful nowcast must beat it.
//
// By default it verifies the latest hour. With --cases it scans the last days
// (Radar-DPC keeps about two weeks), picks the moments with the most rain and
// adds up the scores of all of them. Downloaded files are cached on disk, so
// later runs do not download them again.
//
//	go run ./cmd/nowcastverify
//	go run ./cmd/nowcastverify --cases 20
//	go run ./cmd/nowcastverify --end 2026-10-01T14:00:00Z
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

const userAgent = "radarpoint-nowcastverify/0.1 (+https://github.com/vmaltarello/radarpoint)"

const step = 5 * time.Minute

// thresholds are the rain rates (mm/h) the forecast is scored at: any rain,
// and moderate to heavy rain.
var thresholds = []float32{0.5, 5}

type options struct {
	lead    time.Duration
	history int
	end     string
	cases   int
	every   time.Duration
	days    int
	minRain float64
	cache   string
	nowcast nowcast.Options
}

func main() {
	var o options
	flag.DurationVar(&o.lead, "lead", time.Hour, "longest lead time to verify")
	flag.IntVar(&o.history, "history", 6, "frames used to estimate the motion")
	flag.StringVar(&o.end, "end", "", "single case: time of the last observation (RFC 3339); default the latest")
	flag.IntVar(&o.cases, "cases", 0, "verify this many cases picked from the last days, the rainiest first")
	flag.DurationVar(&o.every, "every", 3*time.Hour, "with --cases: spacing of the candidate moments")
	flag.IntVar(&o.days, "days", 13, "with --cases: how many days back to look")
	flag.Float64Var(&o.minRain, "min-rain", 0.5, "with --cases: skip moments with less rain than this (% of the covered area)")
	flag.StringVar(&o.cache, "cache", defaultCache(), `directory where downloaded files are kept ("" to disable)`)
	o.nowcast = nowcast.DefaultOptions
	flag.BoolVar(&o.nowcast.Robust, "robust", o.nowcast.Robust, "combine frame pairs with a median instead of a mean")
	flag.Float64Var(&o.nowcast.RecencyDecay, "decay", o.nowcast.RecencyDecay, "weight of each older frame pair relative to the next (1 = equal)")
	flag.Float64Var(&o.nowcast.SmoothPerStep, "smooth", o.nowcast.SmoothPerStep, "forecast smoothing growth, pixels per step (0 = none)")
	flag.Float64Var(&o.nowcast.ProbRadius, "prob-radius", o.nowcast.ProbRadius, "probability neighbourhood half side at lead 0, pixels")
	flag.Float64Var(&o.nowcast.ProbRadiusPerStep, "prob-growth", o.nowcast.ProbRadiusPerStep, "probability neighbourhood growth, pixels per step")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "nowcastverify:", err)
		os.Exit(1)
	}
}

func defaultCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "radarpoint", "sri")
}

func run(ctx context.Context, o options) error {
	c := dpc.New(userAgent)
	src := newSource(c, o.cache)
	steps := int(o.lead / step)

	end := time.Time{}
	if o.end != "" {
		t, err := time.Parse(time.RFC3339, o.end)
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
	latestBase := end.Add(-time.Duration(steps) * step)

	bases := []time.Time{latestBase}
	if o.cases > 0 {
		var err error
		if bases, err = pickCases(ctx, src, latestBase, o); err != nil {
			return err
		}
		if len(bases) == 0 {
			return fmt.Errorf("no moment with at least %.1f%% rain in the last %d days", o.minRain, o.days)
		}
	}

	// scores[k][j]: lead k+1, threshold j.
	nc := make([][]table, steps)
	ps := make([][]table, steps)
	rel := make([]reliability, steps)
	for k := range steps {
		nc[k], ps[k] = make([]table, len(thresholds)), make([]table, len(thresholds))
	}

	fmt.Printf("%-17s  %6s  %6s  %21s\n", "case (base, UTC)", "rain", "tracked", "CSI ≥0.5 at +30m")
	fmt.Printf("%-17s  %6s  %6s  %10s %10s\n", "", "", "blocks", "nowcast", "persist.")
	for _, base := range bases {
		src.forget(base.Add(-time.Duration(o.history) * step))
		var in []nowcast.Frame
		for i := o.history - 1; i >= 0; i-- {
			t := base.Add(-time.Duration(i) * step)
			f, err := src.field(ctx, t)
			if err != nil {
				return err
			}
			if f != nil {
				in = append(in, nowcast.Frame{Time: t, Field: f})
			}
		}
		baseField, _ := src.field(ctx, base)
		if baseField == nil {
			fmt.Printf("%s  skipped: no frame at the base time\n", base.Format("2006-01-02 15:04"))
			continue
		}
		n, err := nowcast.Compute(in, src.grid.Projection, src.grid.Transform, step, steps, o.nowcast)
		if err != nil {
			fmt.Printf("%s  skipped: %v\n", base.Format("2006-01-02 15:04"), err)
			continue
		}
		forecasts := n.ForecastFields(n.Latest, steps)
		probs := n.ProbabilityFields(steps)

		var case30 [2]table
		for k := range steps {
			obs, err := src.field(ctx, base.Add(time.Duration(k+1)*step))
			if err != nil {
				return err
			}
			if obs == nil {
				continue
			}
			for j, thr := range thresholds {
				nc[k][j].add(forecasts[k].V, obs.V, thr)
				ps[k][j].add(baseField.V, obs.V, thr)
			}
			rel[k].add(probs[k].V, forecasts[k].V, obs.V, o.nowcast.ProbThreshold)
			if k == 5 {
				case30[0].add(forecasts[k].V, obs.V, 0.5)
				case30[1].add(baseField.V, obs.V, 0.5)
			}
		}
		fmt.Printf("%-17s  %5.1f%%  %6d  %10s %10s\n", base.Format("2006-01-02 15:04"),
			rainShare(baseField.V, 0.5), n.Motion.Measured, num(case30[0].csi()), num(case30[1].csi()))
	}

	fmt.Printf("\nAll %d cases together. CSI: 1 = perfect, 0 = no skill; higher is better.\n", len(bases))
	fmt.Println("lead    ≥0.5 mm/h  nowcast  persist.    ≥5 mm/h  nowcast  persist.")
	for k := range steps {
		fmt.Printf("+%2dm              %7s  %8s               %7s  %8s\n", (k+1)*5,
			num(nc[k][0].csi()), num(ps[k][0].csi()), num(nc[k][1].csi()), num(ps[k][1].csi()))
	}
	printReliability(rel, o.nowcast.ProbThreshold)
	fmt.Fprintf(os.Stderr, "\n%d files downloaded, cache: %s\n", src.fetched, cmp.Or(o.cache, "disabled"))
	fmt.Println("\nData: " + dpc.Attribution)
	return nil
}

// pickCases looks at one frame every o.every over the last o.days and
// returns the o.cases moments with the most rain, oldest first.
func pickCases(ctx context.Context, src *source, latest time.Time, o options) ([]time.Time, error) {
	type candidate struct {
		t    time.Time
		rain float64
	}
	var cands []candidate
	first := latest.Add(-time.Duration(o.days) * 24 * time.Hour)
	total := int(latest.Sub(first)/o.every) + 1
	fmt.Fprintf(os.Stderr, "scanning %d moments over %d days for rain…\n", total, o.days)
	for t := latest; !t.Before(first); t = t.Add(-o.every) {
		f, err := src.field(ctx, t)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		if r := rainShare(f.V, 0.5); r >= o.minRain {
			cands = append(cands, candidate{t, r})
		}
		src.forget(t.Add(time.Nanosecond)) // keep memory low while scanning
	}
	slices.SortFunc(cands, func(a, b candidate) int { return cmp.Compare(b.rain, a.rain) })
	found := len(cands)
	cands = cands[:min(found, o.cases)]
	out := make([]time.Time, len(cands))
	for i, c := range cands {
		out[i] = c.t
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	fmt.Fprintf(os.Stderr, "%d moments with at least %.1f%% rain, verifying %d\n\n", found, o.minRain, len(out))
	return out, nil
}

func num(v float64) string {
	if math.IsNaN(v) {
		return "–"
	}
	return fmt.Sprintf("%.3f", v)
}

// printReliability shows, for a few lead times, how often it rained when a
// given probability was forecast, and the Brier scores.
func printReliability(rel []reliability, threshold float32) {
	leads := []int{2, 5, 11} // +15, +30 and +60 minutes
	fmt.Printf("\nProbability of rain (≥ %g mm/h): observed frequency for each forecast range\n", threshold)
	fmt.Println("forecast        +15m          +30m          +60m")
	for b := range 10 {
		fmt.Printf("%3d–%3d%%  ", b*10, b*10+10)
		for _, k := range leads {
			if k >= len(rel) {
				continue
			}
			fmt.Printf("  %5s %6s", pct(rel[k].observed(b)), share(rel[k].count[b], rel[k].n))
		}
		fmt.Println()
	}
	fmt.Print("Brier score, probability (yes/no forecast); lower is better:")
	for _, k := range leads {
		if k < len(rel) && rel[k].n > 0 {
			fmt.Printf("  %.4f (%.4f)", rel[k].brier/float64(rel[k].n), rel[k].brierYesNo/float64(rel[k].n))
		}
	}
	fmt.Println()
}

func pct(v float64) string {
	if math.IsNaN(v) {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

// share is the fraction of samples in a bin, shown as [x%].
func share(c, n int64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("[%.1f%%]", 100*float64(c)/float64(n))
}
